package server

import (
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sdelcore/shared/internal/web"
)

var siteNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func validSite(name string) bool {
	return siteNameRE.MatchString(name)
}

func (s *Server) siteFromRequest(r *http.Request) string {
	return siteFromHost(r.Host, s.BaseHost)
}

func siteFromHost(host, baseHost string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || strings.EqualFold(host, baseHost) || host == "localhost" {
		return ""
	}
	if net.ParseIP(host) != nil {
		return ""
	}
	i := strings.IndexByte(host, '.')
	if i < 0 {
		return ""
	}
	return host[:i]
}

const notDeployedHTML = `<!doctype html>
<html>
<head><meta charset="utf-8"><title>Site not deployed</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 32rem; margin: 4rem auto;">
<h1>Nothing here yet</h1>
<p>This site hasn't been deployed. From your site directory, run:</p>
<pre>shared deploy</pre>
</body>
</html>
`

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	site := siteFromHost(r.Host, s.BaseHost)
	if site == "" {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(web.HomeHTML))
			return
		}
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if !validSite(site) {
		writeErr(w, http.StatusBadRequest, "invalid site name")
		return
	}

	root, err := filepath.Abs(filepath.Join(s.DataDir, "sites", site))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(notDeployedHTML))
		return
	}

	cleaned := path.Clean("/" + r.URL.Path)
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(cleaned)))
	if err != nil || (target != root && !strings.HasPrefix(target, root+string(filepath.Separator))) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	info, err := os.Stat(target)
	if err == nil && info.IsDir() {
		target = filepath.Join(target, "index.html")
		info, err = os.Stat(target)
	}
	if err != nil || info.IsDir() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	f, err := os.Open(target)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	// Count page views, not asset fetches: only GETs of HTML documents.
	if r.Method == http.MethodGet && strings.HasSuffix(target, ".html") {
		s.meta.countView(site)
	}
	http.ServeContent(w, r, target, info.ModTime(), f)
}

func (s *Server) handleSharedJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(web.SharedJS))
}
