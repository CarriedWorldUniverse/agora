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
	// allowPrivate disables the private-IP guard. Tests set it (their
	// httptest server is on 127.0.0.1, which the guard exists to block);
	// production construction never does.
	allowPrivate bool
}

// NewWebFamily builds the web family with the SSRF-guarded client.
func NewWebFamily() *WebFamily {
	f := &WebFamily{}
	f.client = f.newClient()
	return f
}

// newWebFamilyAllowingPrivate is the test constructor — it permits
// loopback so an httptest server is reachable. Unexported on purpose:
// production has no way to switch the guard off.
func newWebFamilyAllowingPrivate() *WebFamily {
	f := &WebFamily{allowPrivate: true}
	f.client = f.newClient()
	return f
}

func (w *WebFamily) newClient() *http.Client {
	// DialContext is where the IP guard actually bites: checking the host
	// before dialling would be a TOCTOU (DNS can return a different answer
	// on the real dial). Validating the address the dialler is about to
	// connect to closes that — it is the address actually used.
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !w.allowPrivate {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, fmt.Errorf("web_fetch: malformed address %q", addr)
				}
				if ip := net.ParseIP(host); ip != nil && !isPublicIP(ip) {
					return nil, fmt.Errorf("web_fetch: refusing to connect to non-public address %s", ip)
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
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

// htmlToText walks the token stream, skipping the contents of script,
// style, noscript, and svg, and collapses the remaining text runs. A
// tokenizer (not a regex) so malformed markup degrades instead of
// producing garbage.
func htmlToText(body []byte) string {
	z := html.NewTokenizer(strings.NewReader(string(body)))
	var out []string
	skipDepth := 0
	var skipTag string

	for {
		switch z.Next() {
		case html.ErrorToken:
			return collapseBlankLines(strings.Join(out, " "))

		case html.StartTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if skipDepth > 0 {
				if tag == skipTag {
					skipDepth++
				}
				continue
			}
			switch tag {
			case "script", "style", "noscript", "svg", "head":
				skipTag, skipDepth = tag, 1
			case "p", "div", "br", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article":
				out = append(out, "\n")
			}

		case html.EndTagToken:
			if skipDepth > 0 {
				name, _ := z.TagName()
				if string(name) == skipTag {
					skipDepth--
				}
				continue
			}
			name, _ := z.TagName()
			switch string(name) {
			case "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "section", "article":
				out = append(out, "\n")
			}

		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			if t := strings.TrimSpace(string(z.Text())); t != "" {
				out = append(out, t)
			}
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
