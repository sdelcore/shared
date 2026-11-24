package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testServer builds a server in a temp data dir with one deployed site. The
// listener address keeps a port so URL composition is exercised the way a
// non-default port is.
func testServer(t *testing.T, mode, prefix string) *Server {
	t.Helper()
	s, err := New("127.0.0.1:8787", t.TempDir(), "shared.tap", 3, mode, prefix)
	if err != nil {
		t.Fatalf("New(%q, %q): %v", mode, prefix, err)
	}
	dir := filepath.Join(s.DataDir, "sites", "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>notes index</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deep", "page.html"), []byte("<h1>deep</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return s
}

// serve runs one request through the server's router without binding a port.
func serve(s *Server, method, path, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if host != "" {
		req.Host = host
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func TestSubdomainModeUnchanged(t *testing.T) {
	s := testServer(t, URLModeSubdomain, "")

	if rr := serve(s, "GET", "/", "notes.shared.tap"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "notes index") {
		t.Fatalf("site host: got %d %q", rr.Code, rr.Body.String())
	}
	if rr := serve(s, "GET", "/deep/page.html", "notes.shared.tap"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "deep") {
		t.Fatalf("nested asset: got %d %q", rr.Code, rr.Body.String())
	}
	if rr := serve(s, "GET", "/", "shared.tap"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "shared") {
		t.Fatalf("home: got %d", rr.Code)
	}
	// The path prefix is not a route in subdomain mode.
	if rr := serve(s, "GET", "/s/notes/", "shared.tap"); rr.Code != http.StatusNotFound {
		t.Fatalf("prefix in subdomain mode: got %d, want 404", rr.Code)
	}
	// A site host's own /skill.md is the site's file, not the platform's.
	if rr := serve(s, "GET", "/skill.md", "notes.shared.tap"); rr.Code != http.StatusNotFound {
		t.Fatalf("site /skill.md: got %d, want 404", rr.Code)
	}
}

func TestPathModeServesSiteByPath(t *testing.T) {
	s := testServer(t, URLModePath, "")

	if s.PathPrefix != DefaultPathPrefix {
		t.Fatalf("prefix = %q, want %q", s.PathPrefix, DefaultPathPrefix)
	}
	if rr := serve(s, "GET", "/s/notes/", "shared.tap"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "notes index") {
		t.Fatalf("site root: got %d %q", rr.Code, rr.Body.String())
	}
	if rr := serve(s, "GET", "/s/notes/deep/page.html", "shared.tap"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "deep") {
		t.Fatalf("nested asset: got %d %q", rr.Code, rr.Body.String())
	}
	// Relative links need the trailing slash, so the bare form redirects.
	rr := serve(s, "GET", "/s/notes", "shared.tap")
	if rr.Code != http.StatusMovedPermanently || rr.Header().Get("Location") != "/s/notes/" {
		t.Fatalf("bare site: got %d %q", rr.Code, rr.Header().Get("Location"))
	}
	if rr := serve(s, "GET", "/s", "shared.tap"); rr.Code != http.StatusMovedPermanently || rr.Header().Get("Location") != "/s/" {
		t.Fatalf("bare prefix: got %d %q", rr.Code, rr.Header().Get("Location"))
	}
	// A host name does not name a site in path mode.
	if rr := serve(s, "GET", "/", "notes.shared.tap"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "shared") {
		t.Fatalf("site host in path mode: got %d, want the platform home page", rr.Code)
	}
	// A site that is not deployed still answers with the deploy hint.
	if rr := serve(s, "GET", "/s/missing/", "shared.tap"); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "hasn't been deployed") {
		t.Fatalf("undeployed site: got %d %q", rr.Code, rr.Body.String())
	}
	if rr := serve(s, "GET", "/elsewhere", "shared.tap"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown path: got %d, want 404", rr.Code)
	}
}

func TestPathModeScopesTheAPIToTheSite(t *testing.T) {
	s := testServer(t, URLModePath, "")

	rr := serve(s, "GET", "/s/notes/api/db/posts", "shared.tap")
	if rr.Code != 200 {
		t.Fatalf("site-scoped db list: got %d %q", rr.Code, rr.Body.String())
	}
	var body struct {
		Docs []any `json:"docs"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("db list body %q: %v", rr.Body.String(), err)
	}

	// Without a site the site-scoped API must refuse rather than guess.
	if rr := serve(s, "GET", "/api/db/posts", "shared.tap"); rr.Code != http.StatusBadRequest {
		t.Fatalf("unscoped db list: got %d, want 400", rr.Code)
	}
	// The platform API stays available at the root.
	if rr := serve(s, "GET", "/api/sites", "shared.tap"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "notes") {
		t.Fatalf("sites listing: got %d %q", rr.Code, rr.Body.String())
	}
}

func TestPathModeInjectsURLConfig(t *testing.T) {
	s := testServer(t, URLModePath, "/sites")

	rr := serve(s, "GET", "/shared.js", "shared.tap")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `SHARED_URL = {"mode":"path","prefix":"/sites"};`) {
		t.Fatalf("shared.js: got %d", rr.Code)
	}
	// The override must stay a whole statement next to the declared default,
	// or the served file is not valid JavaScript.
	if strings.Contains(rr.Body.String(), `"prefix":"/sites"}{`) {
		t.Fatalf("shared.js override left the default literal dangling")
	}
	// It is also reachable inside the site, and still carries the mode.
	rr = serve(s, "GET", "/sites/notes/shared.js", "shared.tap")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"mode":"path"`) {
		t.Fatalf("site shared.js: got %d", rr.Code)
	}
	if rr := serve(s, "GET", "/sites/", "shared.tap"); rr.Code != 200 || !strings.Contains(rr.Body.String(), `window.__sharedURL={"mode":"path","prefix":"/sites"}`) {
		t.Fatalf("home: got %d", rr.Code)
	}
}

func TestSkillStatesTheLiveURLFormat(t *testing.T) {
	path := testServer(t, URLModePath, "/s")
	rr := serve(path, "GET", "/skill.md", "shared.tap")
	if rr.Code != 200 {
		t.Fatalf("skill.md: got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "## Where sites live on this server") || !strings.Contains(body, "**Path mode.**") {
		t.Fatalf("path-mode skill does not state the URL format")
	}
	if !strings.Contains(body, "/s/<site-name>/") {
		t.Fatalf("path-mode skill does not state the prefix")
	}
	// The same text is what /api/skills/shared-sites returns, since that is what
	// the CLI installs.
	if rr := serve(path, "GET", "/api/skills/shared-sites", "shared.tap"); !strings.Contains(rr.Body.String(), "**Path mode.**") {
		t.Fatalf("/api/skills/shared-sites lacks the URL format section")
	}

	sub := testServer(t, URLModeSubdomain, "")
	rr = serve(sub, "GET", "/skill.md", "shared.tap")
	if !strings.Contains(rr.Body.String(), "**Subdomain mode.**") {
		t.Fatalf("subdomain-mode skill does not state the URL format")
	}
	if strings.Contains(rr.Body.String(), "**Path mode.**") {
		t.Fatalf("subdomain-mode skill claims path mode")
	}
}

func TestSiteURLFollowsTheMode(t *testing.T) {
	sub := testServer(t, URLModeSubdomain, "")
	if got, want := sub.siteURL("notes"), "http://notes.shared.tap:8787/"; got != want {
		t.Fatalf("subdomain URL = %q, want %q", got, want)
	}
	path := testServer(t, URLModePath, "")
	if got, want := path.siteURL("notes"), "http://shared.tap:8787/s/notes/"; got != want {
		t.Fatalf("path URL = %q, want %q", got, want)
	}
}

func TestSitesListingCarriesTheURL(t *testing.T) {
	s := testServer(t, URLModePath, "")
	rr := serve(s, "GET", "/api/sites", "shared.tap")
	var body struct {
		Sites []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"sites"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("sites body %q: %v", rr.Body.String(), err)
	}
	found := false
	for _, site := range body.Sites {
		if site.Name == "notes" {
			found = true
			if site.URL != "http://shared.tap:8787/s/notes/" {
				t.Fatalf("listed URL = %q", site.URL)
			}
		}
	}
	if !found {
		t.Fatalf("notes missing from the listing: %q", rr.Body.String())
	}
}

func TestUploadURLFollowsTheMode(t *testing.T) {
	sub := testServer(t, URLModeSubdomain, "")
	url, err := sub.saveUpload("notes", "a.txt", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "/uploads/notes/") {
		t.Fatalf("subdomain upload URL = %q", url)
	}
	if rr := serve(sub, "GET", url, "notes.shared.tap"); rr.Code != 200 || rr.Body.String() != "hi" {
		t.Fatalf("subdomain upload fetch: got %d %q", rr.Code, rr.Body.String())
	}

	path := testServer(t, URLModePath, "")
	url, err = path.saveUpload("notes", "a.txt", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "/s/notes/uploads/") {
		t.Fatalf("path upload URL = %q", url)
	}
	if rr := serve(path, "GET", url, "shared.tap"); rr.Code != 200 || rr.Body.String() != "hi" {
		t.Fatalf("path upload fetch: got %d %q", rr.Code, rr.Body.String())
	}
}

func TestPathModeRefusesEscapes(t *testing.T) {
	s := testServer(t, URLModePath, "")
	for _, target := range []string{
		"/s/notes/../../etc/passwd",
		"/s/notes/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/s/notes/deep/../../../etc/passwd",
	} {
		if rr := serve(s, "GET", target, "shared.tap"); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d, want 404", target, rr.Code)
		}
	}
	// An invalid site name is rejected rather than served from a sibling dir.
	if rr := serve(s, "GET", "/s/Bad_Name/", "shared.tap"); rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid site name: got %d, want 400", rr.Code)
	}
}

func TestNormalizePathPrefix(t *testing.T) {
	ok := map[string]string{
		"":         DefaultPathPrefix,
		"/s":       "/s",
		"s":        "/s",
		"/sites/":  "/sites",
		"a/b":      "/a/b",
		"/x/y/z":   "/x/y/z",
		"  /trim ": "/trim",
	}
	for in, want := range ok {
		got, err := normalizePathPrefix(in)
		if err != nil || got != want {
			t.Fatalf("normalizePathPrefix(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"/s//x", "/s/../x", "/S!", "/a b"} {
		if got, err := normalizePathPrefix(in); err == nil {
			t.Fatalf("normalizePathPrefix(%q) = %q, want an error", in, got)
		}
	}
}

func TestNormalizeURLMode(t *testing.T) {
	for in, want := range map[string]string{"": URLModeSubdomain, "path": URLModePath, " PATH ": URLModePath, "Subdomain": URLModeSubdomain} {
		got, err := normalizeURLMode(in)
		if err != nil || got != want {
			t.Fatalf("normalizeURLMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizeURLMode("subdomainish"); err == nil {
		t.Fatal("normalizeURLMode accepted an unknown mode")
	}
	if _, err := New("127.0.0.1:8787", t.TempDir(), "shared.tap", 3, "bogus", ""); err == nil {
		t.Fatal("New accepted an unknown URL mode")
	}
}
