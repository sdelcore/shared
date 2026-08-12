package server

import (
	"net/http"

	"github.com/sdelcore/shared/skills"
)

// handleSkillMD serves the shared-sites skill so an agent on any machine can
// read it straight from the server it will deploy to, without cloning the repo
// or trusting a stale local copy. Base host only — see ListenAndServe.
func (s *Server) handleSkillMD(w http.ResponseWriter, r *http.Request) {
	s.writeSkill(w, skills.SharedSites)
}

// handleSkillsList advertises the embedded skills and where to fetch each one.
func (s *Server) handleSkillsList(w http.ResponseWriter, r *http.Request) {
	names := skills.Names()
	out := make([]map[string]string, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]string{"name": name, "url": "/api/skills/" + name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSkillGet(w http.ResponseWriter, r *http.Request) {
	s.writeSkill(w, r.PathValue("name"))
}

func (s *Server) writeSkill(w http.ResponseWriter, name string) {
	body, err := skills.Get(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(body)
}
