package server

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	maxDeployHistory = 50
	maxDailyViewDays = 30
	viewFlushEvery   = 30 * time.Second
)

type deployRecord struct {
	Seq      int64  `json:"seq"`
	Time     string `json:"time"`
	Deployer string `json:"deployer,omitempty"`
	Source   string `json:"source"`
}

type viewStats struct {
	Total     int64            `json:"total"`
	LastVisit string           `json:"lastVisit,omitempty"`
	Daily     map[string]int64 `json:"daily,omitempty"`
}

type siteMeta struct {
	Deploys []deployRecord `json:"deploys"`
	Views   viewStats      `json:"views"`
}

type pendingViews struct {
	count int64
	last  time.Time
}

// metaStore persists per-site metadata (deploy history, view counts) as one
// JSON file per site under <DataDir>/meta. Page views are accumulated in
// memory and flushed periodically so serving traffic never writes to disk.
type metaStore struct {
	mu    sync.Mutex
	dir   string
	cache map[string]*siteMeta
	views map[string]*pendingViews
}

func newMetaStore(dir string) (*metaStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m := &metaStore{
		dir:   dir,
		cache: map[string]*siteMeta{},
		views: map[string]*pendingViews{},
	}
	go m.flushLoop()
	return m, nil
}

func (m *metaStore) path(site string) string {
	return filepath.Join(m.dir, site+".json")
}

// load returns the cached meta for a site, reading it from disk on first use.
// Callers must hold m.mu.
func (m *metaStore) load(site string) *siteMeta {
	if sm, ok := m.cache[site]; ok {
		return sm
	}
	sm := &siteMeta{}
	if data, err := os.ReadFile(m.path(site)); err == nil {
		if err := json.Unmarshal(data, sm); err != nil {
			log.Printf("meta: corrupt %s, starting fresh: %v", m.path(site), err)
			sm = &siteMeta{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Printf("meta: could not read %s: %v", m.path(site), err)
	}
	m.cache[site] = sm
	return sm
}

// Callers must hold m.mu.
func (m *metaStore) persist(site string) {
	sm := m.cache[site]
	b, err := json.MarshalIndent(sm, "", "  ")
	if err != nil {
		log.Printf("meta: could not encode %s: %v", site, err)
		return
	}
	tmp, err := os.CreateTemp(m.dir, "."+site+"-*.tmp")
	if err != nil {
		log.Printf("meta: could not write %s: %v", site, err)
		return
	}
	if _, err := tmp.Write(b); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), m.path(site))
	}
	if err != nil {
		os.Remove(tmp.Name())
		log.Printf("meta: could not write %s: %v", site, err)
	}
}

// current returns the most recent deploy record, or nil if none.
func (m *metaStore) current(site string) *deployRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	sm := m.load(site)
	if len(sm.Deploys) == 0 {
		return nil
	}
	rec := sm.Deploys[len(sm.Deploys)-1]
	return &rec
}

// record appends a deploy record and returns its sequence number.
func (m *metaStore) record(site, deployer, source string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	sm := m.load(site)
	seq := int64(1)
	if n := len(sm.Deploys); n > 0 {
		seq = sm.Deploys[n-1].Seq + 1
	}
	sm.Deploys = append(sm.Deploys, deployRecord{
		Seq:      seq,
		Time:     time.Now().UTC().Format(time.RFC3339),
		Deployer: deployer,
		Source:   source,
	})
	if len(sm.Deploys) > maxDeployHistory {
		sm.Deploys = sm.Deploys[len(sm.Deploys)-maxDeployHistory:]
	}
	m.persist(site)
	return seq
}

func (m *metaStore) stats(site string) (views viewStats, current *deployRecord, deploys int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sm := m.load(site)
	views = sm.Views
	if p := m.views[site]; p != nil {
		views.Total += p.count
	}
	if n := len(sm.Deploys); n > 0 {
		rec := sm.Deploys[n-1]
		current = &rec
		deploys = rec.Seq
	}
	return views, current, deploys
}

func (m *metaStore) drop(site string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, site)
	delete(m.views, site)
	if err := os.Remove(m.path(site)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("meta: could not remove %s: %v", m.path(site), err)
	}
}

// countView records a page view in memory; flushLoop persists it later.
func (m *metaStore) countView(site string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.views[site]
	if p == nil {
		p = &pendingViews{}
		m.views[site] = p
	}
	p.count++
	p.last = time.Now()
}

func (m *metaStore) flushLoop() {
	for range time.Tick(viewFlushEvery) {
		m.flushViews()
	}
}

func (m *metaStore) flushViews() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for site, p := range m.views {
		if p.count == 0 {
			continue
		}
		sm := m.load(site)
		sm.Views.Total += p.count
		sm.Views.LastVisit = p.last.UTC().Format(time.RFC3339)
		if sm.Views.Daily == nil {
			sm.Views.Daily = map[string]int64{}
		}
		sm.Views.Daily[p.last.UTC().Format("2006-01-02")] += p.count
		pruneDaily(sm.Views.Daily, p.last)
		m.persist(site)
		delete(m.views, site)
	}
}

func pruneDaily(daily map[string]int64, now time.Time) {
	cutoff := now.UTC().AddDate(0, 0, -maxDailyViewDays).Format("2006-01-02")
	for day := range daily {
		if day < cutoff {
			delete(daily, day)
		}
	}
}

// sanitizeDeployer trims the client-supplied deployer identity to something
// safe to store and echo back: printable characters, bounded length.
func sanitizeDeployer(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
