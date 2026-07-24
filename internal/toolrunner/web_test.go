package toolrunner

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func webCall(t *testing.T, url string) Call {
	t.Helper()
	args, err := json.Marshal(map[string]any{"url": url})
	if err != nil {
		t.Fatal(err)
	}
	return Call{Name: ToolWebFetch, Args: args}
}

func TestWebFetch_ReturnsReadableTextFromHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>t</title><style>body{color:red}</style></head>
			<body><h1>Hello</h1><p>First para.</p><script>var x=1;</script><p>Second para.</p></body></html>`))
	}))
	defer srv.Close()

	res, err := newWebFamilyAllowingPrivate().Execute(context.Background(), webCall(t, srv.URL))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError on a 200: %s", res.Content)
	}
	for _, want := range []string{"Hello", "First para.", "Second para."} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("content missing %q; got:\n%s", want, res.Content)
		}
	}
	// The whole point of extraction: script/style bodies must not survive.
	for _, unwanted := range []string{"var x=1", "color:red"} {
		if strings.Contains(res.Content, unwanted) {
			t.Errorf("content leaked %q (script/style must be dropped); got:\n%s", unwanted, res.Content)
		}
	}
}

// The page title is usually the most useful single line of a fetched doc
// page, and it lives in <head>. The first version skipped all of <head>
// and threw it away.
func TestHTMLToText_KeepsTitleAsTheFirstLine(t *testing.T) {
	got := htmlToText([]byte(
		`<html><head><title>Rate limits - API docs</title><style>x{}</style></head>` +
			`<body><h1>Overview</h1></body></html>`))
	if !strings.HasPrefix(got, "Rate limits - API docs") {
		t.Fatalf("title is not the first line; got:\n%s", got)
	}
	if !strings.Contains(got, "Overview") {
		t.Fatalf("body text lost while extracting the title; got:\n%s", got)
	}
	if strings.Contains(got, "x{}") {
		t.Fatalf("style body leaked in via the head; got:\n%s", got)
	}
}

// <br/> is a SelfClosingTagToken, not a StartTagToken. The first version
// only handled StartTagToken, so self-closed breaks silently ran their
// surrounding lines together.
func TestHTMLToText_SelfClosingBreakSeparatesLines(t *testing.T) {
	got := htmlToText([]byte(`<p>Line one.<br/>Line two.</p>`))
	if strings.Contains(got, "Line one. Line two.") {
		t.Fatalf("<br/> did not break the line; got:\n%s", got)
	}
	if !strings.Contains(got, "Line one.\nLine two.") {
		t.Fatalf("want the two lines separated by a newline; got:\n%q", got)
	}
}

// Non-HTML content types are already readable — JSON must come back
// verbatim, not mangled by the HTML tokenizer.
func TestWebFetch_NonHTMLPassesThroughVerbatim(t *testing.T) {
	const body = `{"key":"value","n":[1,2,3]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	res, _ := newWebFamilyAllowingPrivate().Execute(context.Background(), webCall(t, srv.URL))
	if !strings.Contains(res.Content, body) {
		t.Fatalf("JSON body not passed through verbatim; got:\n%s", res.Content)
	}
}

// A non-2xx must surface as an error result — otherwise the model treats
// a 404 page as legitimate content.
func TestWebFetch_NonSuccessStatusIsAnErrorResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such page"))
	}))
	defer srv.Close()

	res, _ := newWebFamilyAllowingPrivate().Execute(context.Background(), webCall(t, srv.URL))
	if !res.IsError {
		t.Fatal("404 did not produce an error result")
	}
	if !strings.Contains(res.Content, "no such page") {
		t.Errorf("error body dropped; it often explains the failure. got:\n%s", res.Content)
	}
}

func TestWebFetch_TruncatesAndSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 10000)))
	}))
	defer srv.Close()

	args, _ := json.Marshal(map[string]any{"url": srv.URL, "max_bytes": 100})
	res, _ := newWebFamilyAllowingPrivate().Execute(context.Background(), Call{Name: ToolWebFetch, Args: args})
	if !strings.Contains(res.Content, "truncated") {
		t.Fatalf("truncation not reported — a clipped page must not look complete. got:\n%s", res.Content)
	}
}

// --- security ---

// The SSRF guard: the production constructor must refuse loopback even
// though the server is real and reachable.
func TestWebFetch_ProductionRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("internal service"))
	}))
	defer srv.Close()

	res, err := NewWebFamily().Execute(context.Background(), webCall(t, srv.URL))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("production web_fetch reached a loopback address — SSRF guard is not working: %s", res.Content)
	}
	if strings.Contains(res.Content, "internal service") {
		t.Fatal("body of an internal service leaked through the guard")
	}
}

// The bug this test exists for: the guard originally lived in
// Transport.DialContext, which receives the address as written in the URL.
// net.ParseIP of a HOSTNAME returns nil, so the check silently passed
// every name-based URL and only ever caught literal-IP URLs — and every
// other test here uses httptest, whose URLs are literal 127.0.0.1, so
// they all passed while the guard was bypassable by any DNS name.
//
// "localhost" is the minimal reproduction: a name that resolves to
// loopback. A real attack uses an attacker-controlled domain with an A
// record pointing at 169.254.169.254; the mechanism is identical.
func TestWebFetch_HostnameResolvingToLoopbackIsBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("SECRET INTERNAL BODY"))
	}))
	defer srv.Close()

	// Same server, addressed by NAME rather than by literal IP.
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("splitting test server address: %v", err)
	}
	byName := "http://localhost:" + port + "/"

	res, err := NewWebFamily().Execute(context.Background(), webCall(t, byName))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, "SECRET INTERNAL BODY") {
		t.Fatal("a hostname resolving to loopback reached an internal service — " +
			"the IP guard is not running against the RESOLVED address")
	}
	if !res.IsError {
		t.Fatalf("hostname-to-loopback fetch was not refused: %s", res.Content)
	}
}

func TestWebFetch_RejectsNonHTTPSchemes(t *testing.T) {
	for _, u := range []string{
		"file:///etc/passwd",
		"gopher://example.com/",
		"data:text/plain,hello",
		"ftp://example.com/x",
	} {
		res, err := NewWebFamily().Execute(context.Background(), webCall(t, u))
		if err != nil {
			t.Fatalf("Execute(%s): %v", u, err)
		}
		if !res.IsError {
			t.Errorf("scheme %q was accepted; only http/https may be", u)
		}
	}
}

// isPublicIP is the guard's core predicate — check the ranges directly so
// a regression is caught even if the dial path changes.
func TestIsPublicIP_BlocksInternalRanges(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"::1",             // loopback v6
		"10.0.0.5",        // private A
		"172.16.3.4",      // private B
		"192.168.1.10",    // private C
		"169.254.169.254", // cloud metadata — the classic SSRF target
		"0.0.0.0",         // unspecified
		"224.0.0.1",       // multicast
		"fe80::1",         // link-local v6
		"100.91.185.71",   // CGNAT / tailnet — the personal cloud's own range
	}
	for _, s := range blocked {
		if ip := net.ParseIP(s); ip == nil {
			t.Fatalf("test bug: %q is not a valid IP", s)
		} else if isPublicIP(ip) {
			t.Errorf("isPublicIP(%s) = true; must be blocked", s)
		}
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, s := range allowed {
		if ip := net.ParseIP(s); ip == nil {
			t.Fatalf("test bug: %q is not a valid IP", s)
		} else if !isPublicIP(ip) {
			t.Errorf("isPublicIP(%s) = false; a public address must be allowed", s)
		}
	}
}

// A public host that redirects to an internal address is the cloud-
// metadata attack. The guard must catch the HOP, not just the first URL.
func TestWebFetch_RedirectToInternalAddressIsBlocked(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("SECRET METADATA"))
	}))
	defer internal.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// Production guard on: even the first hop is loopback here, so this
	// asserts the chain never yields the internal body.
	res, _ := NewWebFamily().Execute(context.Background(), webCall(t, redirector.URL))
	if strings.Contains(res.Content, "SECRET METADATA") {
		t.Fatal("redirect chain reached an internal address and leaked its body")
	}
	if !res.IsError {
		t.Fatal("redirect into an internal address should be an error result")
	}
}

func TestWebFetch_RedirectLoopIsBounded(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL, http.StatusFound)
	}))
	defer srv.Close()

	// Must terminate rather than hang; the test timing out IS the failure.
	res, err := newWebFamilyAllowingPrivate().Execute(context.Background(), webCall(t, srv.URL))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatal("an infinite redirect loop should end as an error result")
	}
}

func TestWebFetch_BadArgs(t *testing.T) {
	f := NewWebFamily()
	for _, args := range []string{`{}`, `{"url":""}`, `{"url":"   "}`, `not json`} {
		res, err := f.Execute(context.Background(), Call{Name: ToolWebFetch, Args: json.RawMessage(args)})
		if err != nil {
			t.Fatalf("Execute(%s): %v", args, err)
		}
		if !res.IsError {
			t.Errorf("args %s were accepted; want an error result", args)
		}
	}
}

// --- wiring ---

func TestWebFamily_HandlesAndSpecs(t *testing.T) {
	f := NewWebFamily()
	if f.Name() != contracts.FamilyWeb {
		t.Errorf("Name = %q; want %q", f.Name(), contracts.FamilyWeb)
	}
	if !f.Handles(ToolWebFetch) {
		t.Error("does not handle web_fetch")
	}
	if f.Handles(ToolRunCommand) {
		t.Error("claims to handle run_command")
	}
	specs := f.Specs()
	if len(specs) != 1 || specs[0].Name != ToolWebFetch {
		t.Fatalf("Specs = %+v; want exactly web_fetch", specs)
	}
}

// web_fetch must be approval-gated, and as KindExec specifically — see the
// reasoning in Classify: KindEscalation would be denied outright under the
// never-escalate preset, making it permanently unusable headless.
func TestClassify_WebFetchIsGatedAsExec(t *testing.T) {
	args, _ := json.Marshal(map[string]any{"url": "https://example.com/docs"})
	kind, payload := Classify(Call{Name: ToolWebFetch, Args: args}, Roots{WorkingDir: t.TempDir()})
	if kind != contracts.KindExec {
		t.Fatalf("kind = %q; want %q", kind, contracts.KindExec)
	}
	ep, ok := payload.(ExecPayload)
	if !ok {
		t.Fatalf("payload is %T; want ExecPayload so the existing TUI modal renders it", payload)
	}
	if !strings.Contains(ep.Command, "https://example.com/docs") {
		t.Errorf("payload %q does not name the URL being fetched", ep.Command)
	}
}
