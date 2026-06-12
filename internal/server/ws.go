package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const wsWriteTimeout = 5 * time.Second

type hubConn struct {
	conn *websocket.Conn
}

type Hub struct {
	mu     sync.Mutex
	groups map[string]map[*hubConn]struct{}
}

func NewHub() *Hub {
	return &Hub{groups: make(map[string]map[*hubConn]struct{})}
}

func hubKey(site, channel string) string {
	return site + "/" + channel
}

func (h *Hub) join(site, channel string, c *hubConn) {
	key := hubKey(site, channel)
	h.mu.Lock()
	defer h.mu.Unlock()
	group := h.groups[key]
	if group == nil {
		group = make(map[*hubConn]struct{})
		h.groups[key] = group
	}
	group[c] = struct{}{}
}

func (h *Hub) leave(site, channel string, c *hubConn) {
	key := hubKey(site, channel)
	h.mu.Lock()
	defer h.mu.Unlock()
	group := h.groups[key]
	if group == nil {
		return
	}
	delete(group, c)
	if len(group) == 0 {
		delete(h.groups, key)
	}
}

func (h *Hub) broadcast(site, channel string, sender *hubConn, msg []byte) {
	key := hubKey(site, channel)
	h.mu.Lock()
	members := make([]*hubConn, 0, len(h.groups[key]))
	for c := range h.groups[key] {
		if c != sender {
			members = append(members, c)
		}
	}
	h.mu.Unlock()

	for _, c := range members {
		ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
		err := c.conn.Write(ctx, websocket.MessageText, msg)
		cancel()
		if err != nil {
			h.leave(site, channel, c)
			c.conn.Close(websocket.StatusPolicyViolation, "write failed")
		}
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	site := s.siteFromRequest(r)
	if site == "" || !validSite(site) {
		writeErr(w, http.StatusBadRequest, "invalid or missing site")
		return
	}
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = "default"
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(64 << 10)

	c := &hubConn{conn: conn}
	s.hub.join(site, channel, c)
	defer func() {
		s.hub.leave(site, channel, c)
		conn.Close(websocket.StatusNormalClosure, "")
	}()

	ctx := r.Context()
	for {
		typ, msg, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		s.hub.broadcast(site, channel, c, msg)
	}
}
