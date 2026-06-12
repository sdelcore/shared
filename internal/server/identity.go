package server

import (
	"net/http"
	"os"
	"path/filepath"
)

func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(s.DataDir, "identity.json")
	if data, err := os.ReadFile(path); err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
		return
	}
	user := os.Getenv("SHARED_USER")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "shared"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"email": user + "@localhost",
		"name":  user,
	})
}
