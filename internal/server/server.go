package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sdelcore/shared/internal/store"
)

type Server struct {
	Addr, DataDir, BaseHost string
	KeepVersions            int

	store *store.Store
	meta  *metaStore
	hub   *Hub
	api   *http.ServeMux
	ai    *limiter
}

func New(addr, dataDir, baseHost string, keepVersions int) (*Server, error) {
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

	return s, nil
}

func (s *Server) ListenAndServe() error {
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})
	return http.ListenAndServe(s.Addr, root)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
