package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

type Doc = map[string]any

type Event struct {
	Type string `json:"type"`
	Doc  Doc    `json:"doc"`
}

var ErrNotFound = errors.New("not found")

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func ValidName(name string) bool {
	return nameRe.MatchString(name)
}

type Store struct {
	dir string

	mu      sync.RWMutex
	data    map[string]map[string]map[string]Doc
	subs    map[string]map[int]chan Event
	nextSub int
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{
		dir:  dir,
		data: make(map[string]map[string]map[string]Doc),
		subs: make(map[string]map[int]chan Event),
	}, nil
}

func (s *Store) Create(site, col string, data map[string]any) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm, err := s.ensure(site, col)
	if err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	doc := copyDoc(data)
	doc["id"] = id
	doc["createdAt"] = now
	doc["updatedAt"] = now
	cm[id] = doc
	if err := s.persist(site, col, cm); err != nil {
		delete(cm, id)
		return nil, err
	}
	s.notify(site, col, Event{Type: "created", Doc: doc})
	return copyDoc(doc), nil
}

func (s *Store) List(site, col string) ([]Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm, err := s.ensure(site, col)
	if err != nil {
		return nil, err
	}
	docs := make([]Doc, 0, len(cm))
	for _, d := range cm {
		docs = append(docs, copyDoc(d))
	}
	sortDocs(docs)
	return docs, nil
}

func (s *Store) Get(site, col, id string) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm, err := s.ensure(site, col)
	if err != nil {
		return nil, err
	}
	doc, ok := cm[id]
	if !ok {
		return nil, ErrNotFound
	}
	return copyDoc(doc), nil
}

func (s *Store) Update(site, col, id string, data map[string]any) (Doc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm, err := s.ensure(site, col)
	if err != nil {
		return nil, err
	}
	old, ok := cm[id]
	if !ok {
		return nil, ErrNotFound
	}
	doc := copyDoc(old)
	for k, v := range data {
		doc[k] = v
	}
	doc["id"] = old["id"]
	doc["createdAt"] = old["createdAt"]
	doc["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	cm[id] = doc
	if err := s.persist(site, col, cm); err != nil {
		cm[id] = old
		return nil, err
	}
	s.notify(site, col, Event{Type: "updated", Doc: doc})
	return copyDoc(doc), nil
}

func (s *Store) Delete(site, col, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cm, err := s.ensure(site, col)
	if err != nil {
		return err
	}
	doc, ok := cm[id]
	if !ok {
		return ErrNotFound
	}
	delete(cm, id)
	if err := s.persist(site, col, cm); err != nil {
		cm[id] = doc
		return err
	}
	s.notify(site, col, Event{Type: "deleted", Doc: doc})
	return nil
}

func (s *Store) Subscribe(site, col string) (<-chan Event, func()) {
	key := site + "/" + col
	ch := make(chan Event, 64)
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	if s.subs[key] == nil {
		s.subs[key] = make(map[int]chan Event)
	}
	s.subs[key][id] = ch
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		m := s.subs[key]
		if c, ok := m[id]; ok {
			delete(m, id)
			if len(m) == 0 {
				delete(s.subs, key)
			}
			close(c)
		}
	}
	return ch, cancel
}

func (s *Store) notify(site, col string, ev Event) {
	for _, ch := range s.subs[site+"/"+col] {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Store) ensure(site, col string) (map[string]Doc, error) {
	if !ValidName(site) || !ValidName(col) {
		return nil, fmt.Errorf("invalid name %q/%q", site, col)
	}
	sm := s.data[site]
	if sm == nil {
		sm = make(map[string]map[string]Doc)
		s.data[site] = sm
	}
	if cm, ok := sm[col]; ok {
		return cm, nil
	}
	cm := make(map[string]Doc)
	b, err := os.ReadFile(s.path(site, col))
	switch {
	case err == nil:
		var docs []Doc
		if err := json.Unmarshal(b, &docs); err != nil {
			return nil, err
		}
		for _, d := range docs {
			if id, _ := d["id"].(string); id != "" {
				cm[id] = d
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	sm[col] = cm
	return cm, nil
}

func (s *Store) persist(site, col string, cm map[string]Doc) error {
	docs := make([]Doc, 0, len(cm))
	for _, d := range cm {
		docs = append(docs, d)
	}
	sortDocs(docs)
	b, err := json.MarshalIndent(docs, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(s.dir, site)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+col+"-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.path(site, col)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (s *Store) path(site, col string) string {
	return filepath.Join(s.dir, site, col+".json")
}

func sortDocs(docs []Doc) {
	sort.Slice(docs, func(i, j int) bool {
		a, _ := docs[i]["createdAt"].(string)
		b, _ := docs[j]["createdAt"].(string)
		if a != b {
			return a < b
		}
		ai, _ := docs[i]["id"].(string)
		bi, _ := docs[j]["id"].(string)
		return ai < bi
	})
}

func copyDoc(d Doc) Doc {
	out := make(Doc, len(d)+3)
	for k, v := range d {
		out[k] = v
	}
	return out
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
