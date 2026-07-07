package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/sdelcore/shared/internal/store"
)

func (s *Server) dbScope(w http.ResponseWriter, r *http.Request) (site, col string, ok bool) {
	site = s.siteFromRequest(r)
	if site == "" || !validSite(site) {
		writeErr(w, http.StatusBadRequest, "invalid site")
		return "", "", false
	}
	col = r.PathValue("collection")
	if !store.ValidName(col) {
		writeErr(w, http.StatusBadRequest, "invalid collection")
		return "", "", false
	}
	return site, col, true
}

func decodeDoc(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var data map[string]any
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return nil, false
	}
	return data, true
}

func dbErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

func (s *Server) handleDBList(w http.ResponseWriter, r *http.Request) {
	site, col, ok := s.dbScope(w, r)
	if !ok {
		return
	}
	docs, err := s.store.List(site, col)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"docs": docs})
}

func (s *Server) handleDBCreate(w http.ResponseWriter, r *http.Request) {
	site, col, ok := s.dbScope(w, r)
	if !ok {
		return
	}
	data, ok := decodeDoc(w, r)
	if !ok {
		return
	}
	doc, err := s.store.Create(site, col, data)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, doc)
}

func (s *Server) handleDBGet(w http.ResponseWriter, r *http.Request) {
	site, col, ok := s.dbScope(w, r)
	if !ok {
		return
	}
	doc, err := s.store.Get(site, col, r.PathValue("id"))
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleDBUpdate(w http.ResponseWriter, r *http.Request) {
	site, col, ok := s.dbScope(w, r)
	if !ok {
		return
	}
	data, ok := decodeDoc(w, r)
	if !ok {
		return
	}
	doc, err := s.store.Update(site, col, r.PathValue("id"), data)
	if err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleDBDelete(w http.ResponseWriter, r *http.Request) {
	site, col, ok := s.dbScope(w, r)
	if !ok {
		return
	}
	if err := s.store.Delete(site, col, r.PathValue("id")); err != nil {
		dbErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleDBSubscribe(w http.ResponseWriter, r *http.Request) {
	site, col, ok := s.dbScope(w, r)
	if !ok {
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.wsOriginPatterns(r)})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	events, cancel := s.store.Subscribe(site, col)
	defer cancel()

	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	go func() {
		defer stop()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case ev, ok := <-events:
			if !ok {
				conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			if err := wsjson.Write(ctx, conn, ev); err != nil {
				return
			}
		}
	}
}
