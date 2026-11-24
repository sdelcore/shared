package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// A site can be addressed two ways, and the choice is deployment
// configuration rather than a property of any one site:
//
//	subdomain — <site>.<baseHost>, the original scheme. It needs wildcard DNS
//	            and a wildcard certificate.
//	path      — <baseHost><prefix>/<site>/, for hosts where you do not control
//	            the DNS: a Tailscale machine name, a funnel, an IP address, or a
//	            proxy that only routes on the path.
//
// Every site on a server uses the same scheme, and the skill the server hands
// out states the live one, so an agent reads the URL format off the server it
// is deploying to instead of assuming subdomains.
const (
	URLModeSubdomain = "subdomain"
	URLModePath      = "path"
)

// DefaultPathPrefix is used when path mode is configured without one.
const DefaultPathPrefix = "/s"

// urlConfigMarker appears in the embedded pages and the SDK. The server
// replaces it with the live mode so one asset serves either scheme.
const urlConfigHTMLMarker = "<!--__SHARED_URL_CONFIG__-->"
const urlConfigJSMarker = "/*__SHARED_URL_CONFIG__*/"

type siteContextKey struct{}

// withSite pins the site a request belongs to. In path mode the router resolves
// it from the URL prefix; handlers read it back through siteFromRequest.
func withSite(r *http.Request, site string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), siteContextKey{}, site))
}

func normalizeURLMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", URLModeSubdomain:
		return URLModeSubdomain, nil
	case URLModePath:
		return URLModePath, nil
	}
	return "", fmt.Errorf("invalid URL mode %q (want %q or %q)", mode, URLModeSubdomain, URLModePath)
}

// normalizePathPrefix canonicalises a configured prefix to the form the router
// matches: one leading slash, no trailing slash, no empty or traversal
// segments. Each segment follows the site-name rules, since the segment after
// the prefix is a site name.
func normalizePathPrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return DefaultPathPrefix, nil
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		return DefaultPathPrefix, nil
	}
	for _, seg := range strings.Split(strings.TrimPrefix(prefix, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." || !validSite(seg) {
			return "", fmt.Errorf("invalid path prefix %q (each segment must match %s)", prefix, siteNameRE.String())
		}
	}
	return prefix, nil
}

// splitPathURL splits a path-mode request path into the site it addresses and
// the site-relative remainder: "/s/notes/a.css" -> ("notes", "/a.css", true).
// matched reports whether the path sits under the prefix at all; rest is empty
// when the site was addressed without a trailing slash.
func (s *Server) splitPathURL(p string) (site, rest string, matched bool) {
	if s.URLMode != URLModePath {
		return "", "", false
	}
	if !strings.HasPrefix(p, s.PathPrefix+"/") {
		return "", "", false
	}
	remainder := strings.TrimPrefix(p, s.PathPrefix+"/")
	if remainder == "" {
		return "", "", false
	}
	site = remainder
	if i := strings.IndexByte(remainder, '/'); i >= 0 {
		site = remainder[:i]
		rest = remainder[i:]
	}
	return site, rest, true
}

// urlConfigJSON is the configuration the embedded pages and SDK need. It is
// injected at serve time rather than baked in, because one binary serves both
// schemes.
func (s *Server) urlConfigJSON() string {
	b, _ := json.Marshal(map[string]string{"mode": s.URLMode, "prefix": s.PathPrefix})
	return string(b)
}

// injectURLConfig replaces the marker in an embedded asset with the live URL
// configuration. An asset without the marker is served untouched.
func (s *Server) injectURLConfig(body []byte, html bool) []byte {
	marker := urlConfigJSMarker
	// The SDK declares its subdomain-mode default in the file and the server
	// overrides it here, so the asset stays valid JavaScript on its own.
	replacement := "SHARED_URL = " + s.urlConfigJSON() + ";"
	if html {
		marker = urlConfigHTMLMarker
		replacement = "<script>window.__sharedURL=" + s.urlConfigJSON() + ";</script>"
	}
	text := string(body)
	if !strings.Contains(text, marker) {
		return body
	}
	return []byte(strings.Replace(text, marker, replacement, 1))
}

// urlFormatSection is appended to the shared-sites skill when it is served, so
// the agent installing or reading the skill learns which URL format this server
// actually uses. It is content, not a runbook note.
func (s *Server) urlFormatSection() string {
	if s.URLMode == URLModePath {
		return "\n## Where sites live on this server\n\n" +
			"**Path mode.** Every site shares one host and is addressed by its path:\n\n" +
			"    http://" + s.BaseHost + s.PathPrefix + "/<site-name>/\n\n" +
			"There are no per-site subdomains here — the host only routes on the path, so a\n" +
			"deploy of `mysite` is at `" + s.PathPrefix + "/mysite/`, and its `index.html` is at\n" +
			"`" + s.PathPrefix + "/mysite/index.html`. `shared deploy` prints the exact URL; use it\n" +
			"rather than composing one. Inside a page the SDK is unchanged — keep\n" +
			"`<script src=\"/shared.js\"></script>` and every `/api/...` call resolves against the\n" +
			"site's own prefix automatically. Links between sites must include the prefix too\n" +
			"(`" + s.PathPrefix + "/other-site/`), and a page must not hardcode a host-absolute\n" +
			"`/api/...` path of its own.\n"
	}
	return "\n## Where sites live on this server\n\n" +
		"**Subdomain mode.** Every site is its own host: `http://<site-name>." + s.BaseHost + "/`.\n" +
		"`shared deploy` prints the exact URL; use it rather than composing one. The SDK is\n" +
		"loaded with `<script src=\"/shared.js\"></script>` and its `/api/...` calls are scoped\n" +
		"to the current site by the host name.\n"
}
