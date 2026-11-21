package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sdelcore/shared/internal/store"
	"github.com/sdelcore/shared/internal/web"
)

type Server struct {
	Addr, DataDir, BaseHost string
	KeepVersions            int
	URLMode                 string
	PathPrefix              string

	store *store.Store
	meta  *metaStore
	hub   *Hub
	api   *http.ServeMux
	ai    *limiter
}

func New(addr, dataDir, baseHost string, keepVersions int, urlMode, pathPrefix string) (*Server, error) {
	mode, err := normalizeURLMode(urlMode)
	if err != nil {
		return nil, err
	}
	prefix, err := normalizePathPrefix(pathPrefix)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{filepath.Join(dataDir, "sites"), filepath.Join(dataDir, "uploads"), filepath.Join(dataDir, "versions")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	st, err := store.Open(filepath.Join(dataDir, "db"))
	if err != nil {
		return nil, err
	}
	meta, err := newMetaStore(filepath.Join(dataDir, "meta"))
	if err != nil {
		return nil, err
	}
	s := &Server{
		Addr:         addr,
		DataDir:      dataDir,
		BaseHost:     baseHost,
		KeepVersions: keepVersions,
		URLMode:      mode,
		PathPrefix:   prefix,
		store:        st,
		meta:         meta,
		hub:          NewHub(),
		api:          http.NewServeMux(),
		ai:           newAILimiter(),
	}
	s.sweep()

	s.api.HandleFunc("POST /api/deploy", s.handleDeploy)
	s.api.HandleFunc("POST /api/rollback", s.handleRollback)
	s.api.HandleFunc("GET /api/versions", s.handleVersions)
	s.api.HandleFunc("GET /api/sites", s.handleSites)
	s.api.HandleFunc("DELETE /api/sites/{name}", s.handleDeleteSite)
	s.api.HandleFunc("GET /api/export", s.handleExport)
	s.api.HandleFunc("GET /api/db/{collection}", s.handleDBList)
	s.api.HandleFunc("POST /api/db/{collection}", s.handleDBCreate)
	s.api.HandleFunc("GET /api/db/{collection}/subscribe", s.handleDBSubscribe)
	s.api.HandleFunc("GET /api/db/{collection}/{id}", s.handleDBGet)
	s.api.HandleFunc("PUT /api/db/{collection}/{id}", s.handleDBUpdate)
	s.api.HandleFunc("DELETE /api/db/{collection}/{id}", s.handleDBDelete)
	s.api.HandleFunc("POST /api/ai/chat", s.aiRateLimit(s.handleAIChat))
	s.api.HandleFunc("POST /api/ai/image", s.aiRateLimit(s.handleAIImage))
	s.api.HandleFunc("POST /api/uploads", s.handleUpload)
	s.api.HandleFunc("GET /api/identity", s.handleIdentity)
	s.api.HandleFunc("GET /api/ws", s.handleWS)
	s.api.HandleFunc("GET /api/skills", s.handleSkillsList)
	s.api.HandleFunc("GET /api/skills/{name}", s.handleSkillGet)

	return s, nil
}

func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.Addr, s.Handler())
}

// Handler exposes the router so tests (and any embedding process) can serve the
// platform without binding a port.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.URLMode == URLModePath {
			s.routePath(w, r)
			return
		}
		s.routeHost(w, r)
	})
}

// routeHost is subdomain mode, where the host names the site.
func (s *Server) routeHost(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/"):
		s.api.ServeHTTP(w, r)
	case r.URL.Path == "/shared.js":
		s.handleSharedJS(w, r)
	case r.URL.Path == "/favicon.svg" && siteFromHost(r.Host, s.BaseHost) == "":
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(web.FaviconSVG)
	// Base host only: on a site host this path belongs to the site, and
	// intercepting it would shadow a deployed file of the same name.
	case r.URL.Path == "/skill.md" && siteFromHost(r.Host, s.BaseHost) == "":
		s.handleSkillMD(w, r)
	case strings.HasPrefix(r.URL.Path, "/uploads/"):
		s.handleServeUpload(w, r)
	default:
		s.handleStatic(w, r)
	}
}

// routePath is path mode, where the site name is the first segment under the
// prefix and the platform itself lives outside it.
func (s *Server) routePath(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch p {
	case s.PathPrefix:
		http.Redirect(w, r, s.PathPrefix+"/", http.StatusMovedPermanently)
		return
	case s.PathPrefix + "/", "/":
		s.writeHome(w)
		return
	}

	if site, rest, matched := s.splitPathURL(p); matched {
		if !validSite(site) {
			writeErr(w, http.StatusBadRequest, "invalid site name")
			return
		}
		// Without the trailing slash the page's relative links would resolve
		// one level too high, so every site URL is canonical with it.
		if rest == "" {
			http.Redirect(w, r, s.PathPrefix+"/"+site+"/", http.StatusMovedPermanently)
			return
		}
		// Rewriting the path to the site-relative remainder lets the existing
		// handlers run unchanged: the site comes from the request context.
		r = withSite(r, site)
		r.URL.Path = rest
		s.routeSite(w, r)
		return
	}

	switch {
	case strings.HasPrefix(p, "/api/"):
		s.api.ServeHTTP(w, r)
	case p == "/shared.js":
		s.handleSharedJS(w, r)
	case p == "/favicon.svg":
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(web.FaviconSVG)
	case p == "/skill.md":
		s.handleSkillMD(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

// routeSite dispatches a request already scoped to one site.
func (s *Server) routeSite(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/"):
		s.api.ServeHTTP(w, r)
	case r.URL.Path == "/shared.js":
		s.handleSharedJS(w, r)
	case strings.HasPrefix(r.URL.Path, "/uploads/"):
		s.handleServeUpload(w, r)
	default:
		s.handleStatic(w, r)
	}
}

func (s *Server) writeHome(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.injectURLConfig(web.HomeHTML, true))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
