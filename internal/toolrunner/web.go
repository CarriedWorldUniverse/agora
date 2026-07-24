package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/html"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// web.go is the web family: one tool, web_fetch, that turns a URL into
// readable text. Before this the only way to reach the network was shelling
// curl through run_command, which returns raw HTML the model has to parse
// and is gated as an arbitrary shell command.
//
// The security posture is the interesting part. A URL is attacker-
// influenceable in a way a local path is not: page content the model reads
// can name the NEXT url to fetch, so a prompt-injected page can try to
// steer the agent at internal services (SSRF). Three defences, all
// enforced here rather than left to the approval layer:
//
//  1. http/https only — no file://, no gopher://, no data:.
//  2. Every resolved IP is checked against the private/loopback/link-local
//     ranges and rejected, INCLUDING on each redirect hop (a public host
//     that 302s to 169.254.169.254 is the classic cloud-metadata attack).
//  3. Hard caps on body size, redirects, and time.
//
// The approval layer still gates the call (Classify → KindExec); this is
// defence in depth, not a substitute.

const (
	// ToolWebFetch fetches a URL and returns its readable text.
	ToolWebFetch = "web_fetch"

	// DefaultWebTimeout bounds one fetch including redirects.
	DefaultWebTimeout = 30 * time.Second
	// DefaultWebMaxBytes caps the response body read into memory. Chosen to
	// hold a large article without letting one fetch blow the context
	// window (the text extraction shrinks it further).
	DefaultWebMaxBytes = 5 << 20 // 5 MiB
	// MaxWebRedirects bounds a redirect chain; each hop is re-validated.
	MaxWebRedirects = 5
)

// WebFamily serves web_fetch. The zero value is not usable — NewWebFamily
// builds the guarded http.Client.
type WebFamily struct {
	client *http.Client
	// allowLoopback narrowly permits LOOPBACK addresses, and nothing else,
	// so tests can reach their own httptest server. Production construction
	// never sets it.
	//
	// It is deliberately not a blanket "skip the guard" flag. An escape
	// hatch that disabled the whole check would mean every test using it
	// proved nothing about SSRF — and would keep passing if the flag ever
	// leaked into production. Scoped this way, link-local (cloud metadata),
	// private ranges and CGNAT stay refused even under test, so those
	// properties are covered by the SAME code path production runs.
	allowLoopback bool
}

// NewWebFamily builds the web family with the SSRF-guarded client.
func NewWebFamily() *WebFamily {
	f := &WebFamily{}
	f.client = f.newClient()
	return f
}

// newWebFamilyAllowingLoopback is the test constructor: it permits
// loopback so an httptest server is reachable, and relaxes nothing else.
// Unexported on purpose — production has no way to reach it.
func newWebFamilyAllowingLoopback() *WebFamily {
	f := &WebFamily{allowLoopback: true}
	f.client = f.newClient()
	return f
}

func (w *WebFamily) newClient() *http.Client {
	// The IP guard lives in Dialer.Control, NOT in Transport.DialContext.
	// This distinction is the whole security property:
	//
	//   DialContext receives the address as written in the URL —
	//   "localhost:80", "evil.example.com:443". net.ParseIP of a HOSTNAME
	//   returns nil, so a check there silently passes every name-based URL
	//   and only ever catches literal-IP URLs. An attacker publishing an A
	//   record pointing at 169.254.169.254 walks straight through.
	//
	//   Control runs after resolution, once per candidate address, with the
	//   real IP ("127.0.0.1:80", "[::1]:80") — and before connect(2), so
	//   there is no TOCTOU and no rebinding window. Rejecting here rejects
	//   the address actually about to be used.
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_ string, address string, _ syscall.RawConn) error {
			return w.guardResolvedAddr(address)
		},
	}
	transport := &http.Transport{DialContext: dialer.DialContext}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= MaxWebRedirects {
				return fmt.Errorf("web_fetch: too many redirects (>%d)", MaxWebRedirects)
			}
			// Re-check the scheme on every hop: an https page redirecting to
			// file:// or data:// must not slip past the initial check.
			if err := checkWebScheme(req.URL); err != nil {
				return err
			}
			return nil
		},
	}
}

// guardResolvedAddr is the SSRF check, applied to a RESOLVED "ip:port"
// address. It is a named method rather than an inline closure so it can be
// table-tested directly over every address class, with no network and no
// DNS — the original bug survived review precisely because the only way to
// reach the guard was through httptest, whose URLs are literal 127.0.0.1,
// so the tests could only ever exercise the one case that already worked.
func (w *WebFamily) guardResolvedAddr(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("web_fetch: malformed address %q", address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Control is documented to receive a resolved address. If we cannot
		// parse it, refuse rather than assume public — this is also the
		// backstop that would have caught the original bug, where a
		// hostname reached the check and fell through as allowed.
		return fmt.Errorf("web_fetch: could not parse resolved address %q", address)
	}
	// The narrow test seam: loopback only, and only when explicitly built
	// for tests. Everything else is judged by the production rule.
	if ip.IsLoopback() && w.allowLoopback {
		return nil
	}
	if !isPublicIP(ip) {
		return fmt.Errorf("web_fetch: refusing to connect to non-public address %s", ip)
	}
	return nil
}

func (w *WebFamily) Name() string { return contracts.FamilyWeb }

func (w *WebFamily) Handles(name string) bool { return name == ToolWebFetch }

func (w *WebFamily) Specs() []contracts.ToolSpec {
	return []contracts.ToolSpec{
		{
			Name: ToolWebFetch,
			Description: "Fetch a URL over http/https and return its readable text " +
				"(HTML is reduced to text; other text types are returned as-is).",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "Absolute http:// or https:// URL to fetch."},
					"max_bytes": map[string]any{
						"type":        "integer",
						"description": fmt.Sprintf("Max response bytes to read (default %d).", DefaultWebMaxBytes),
					},
				},
				"required": []string{"url"},
			}),
		},
	}
}

type webFetchArgs struct {
	URL      string `json:"url"`
	MaxBytes int    `json:"max_bytes"`
}

func (w *WebFamily) Execute(ctx context.Context, call Call) (Result, error) {
	if call.Name != ToolWebFetch {
		return errorResult(fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)), nil
	}
	var a webFetchArgs
	if err := json.Unmarshal(call.Args, &a); err != nil || strings.TrimSpace(a.URL) == "" {
		return errorResult(fmt.Errorf("%w: web_fetch", ErrBadArgs)), nil
	}

	u, err := url.Parse(strings.TrimSpace(a.URL))
	if err != nil {
		return errorResult(fmt.Errorf("%w: web_fetch: %v", ErrBadArgs, err)), nil
	}
	if err := checkWebScheme(u); err != nil {
		return errorResult(err), nil
	}

	maxBytes := DefaultWebMaxBytes
	if a.MaxBytes > 0 && a.MaxBytes < DefaultWebMaxBytes {
		maxBytes = a.MaxBytes
	}

	fetchCtx, cancel := context.WithTimeout(ctx, DefaultWebTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return errorResult(fmt.Errorf("web_fetch: %v", err)), nil
	}
	// Identify honestly rather than impersonating a browser: sites that
	// want to refuse automated traffic should be able to.
	req.Header.Set("User-Agent", "agora/1.0 (+https://github.com/CarriedWorldUniverse/agora)")
	req.Header.Set("Accept", "text/html,text/plain,application/json;q=0.9,*/*;q=0.5")

	resp, err := w.client.Do(req)
	if err != nil {
		return errorResult(fmt.Errorf("web_fetch: %v", err)), nil
	}
	defer resp.Body.Close()

	// Read one byte past the cap so truncation can be reported honestly
	// rather than silently handing back a clipped page as if complete.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return errorResult(fmt.Errorf("web_fetch: reading body: %v", err)), nil
	}
	truncated := len(body) > maxBytes
	if truncated {
		body = body[:maxBytes]
	}

	ctype := resp.Header.Get("Content-Type")
	text := extractText(body, ctype)

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", resp.Request.URL.String(), resp.Status)
	if truncated {
		fmt.Fprintf(&b, "[truncated at %d bytes]\n", maxBytes)
	}
	b.WriteString("\n")
	b.WriteString(text)

	// A non-2xx is reported as an error result so the model sees the
	// failure rather than treating an error page as content — but the body
	// is still included, since 404/403 pages often explain what to do.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{Content: b.String(), IsError: true}, nil
	}
	return Result{Content: b.String()}, nil
}

// checkWebScheme enforces http/https only.
func checkWebScheme(u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return fmt.Errorf("web_fetch: URL has no host: %s", u.String())
		}
		return nil
	default:
		return fmt.Errorf("web_fetch: unsupported scheme %q (only http and https)", u.Scheme)
	}
}

// isPublicIP reports whether ip is safe to fetch from — i.e. NOT loopback,
// private, link-local (which covers cloud metadata at 169.254.169.254),
// multicast, or unspecified.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// Carrier-grade NAT (100.64.0.0/10) is not covered by IsPrivate but is
	// just as internal in practice — this is the tailnet range the whole
	// personal cloud runs on.
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return false
	}
	return true
}

// extractText reduces a response body to something worth putting in a
// context window. HTML goes through the tokenizer with script/style
// dropped; everything else is returned as-is (JSON, plain text, markdown
// are all already readable).
func extractText(body []byte, contentType string) string {
	if !strings.Contains(strings.ToLower(contentType), "text/html") {
		return string(body)
	}
	return htmlToText(body)
}

// blockTags break the text flow — each one starts a new line so the
// extracted text keeps the page's structure instead of running together.
var blockTags = map[string]bool{
	"p": true, "div": true, "li": true, "tr": true, "section": true,
	"article": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "blockquote": true, "pre": true,
}

// skipTags have contents that are code or graphics, never prose.
var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "svg": true,
}

// htmlToText walks the token stream, skipping the contents of script,
// style, noscript, and svg, and collapses the remaining text runs. A
// tokenizer (not a regex) so malformed markup degrades instead of
// producing garbage.
//
// <title> is pulled out and used as the first line: for a fetched doc page
// it is usually the single most useful piece of orientation, and it lives
// inside <head>, which carries no other prose.
func htmlToText(body []byte) string {
	z := html.NewTokenizer(strings.NewReader(string(body)))
	var out []string
	var title string
	skipDepth := 0
	var skipTag string
	inTitle := false

	for {
		switch z.Next() {
		case html.ErrorToken:
			text := collapseBlankLines(strings.Join(out, " "))
			if title != "" && text != "" {
				return title + "\n\n" + text
			}
			if title != "" {
				return title
			}
			return text

		case html.StartTagToken:
			tagName, _ := z.TagName()
			tag := string(tagName)
			if skipDepth > 0 {
				if tag == skipTag {
					skipDepth++
				}
				continue
			}
			switch {
			case skipTags[tag]:
				skipTag, skipDepth = tag, 1
			case tag == "title":
				inTitle = true
			case tag == "br":
				out = append(out, "\n")
			case blockTags[tag]:
				out = append(out, "\n")
			}

		case html.SelfClosingTagToken:
			// <br/> arrives as its OWN token type, not StartTagToken — the
			// first version only handled StartTagToken, so self-closed
			// breaks silently ran their surrounding lines together.
			if skipDepth > 0 {
				continue
			}
			tagName, _ := z.TagName()
			if tag := string(tagName); tag == "br" || blockTags[tag] {
				out = append(out, "\n")
			}

		case html.EndTagToken:
			tagName, _ := z.TagName()
			tag := string(tagName)
			if skipDepth > 0 {
				if tag == skipTag {
					skipDepth--
				}
				continue
			}
			if tag == "title" {
				inTitle = false
			}
			if blockTags[tag] {
				out = append(out, "\n")
			}

		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			t := strings.TrimSpace(string(z.Text()))
			if t == "" {
				continue
			}
			if inTitle {
				if title == "" {
					title = t
				}
				continue
			}
			out = append(out, t)
		}
	}
}

// collapseBlankLines tidies the joined token text: trim each line, drop
// runs of blank lines down to one. Without this the output is mostly
// whitespace from the block-tag newlines.
func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := false
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, ln)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
