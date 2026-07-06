package server

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxDeploySize = 256 << 20
	maxFileSize   = 128 << 20
)

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	if site == "" {
		site = r.Header.Get("X-Shared-Site")
	}
	if !validSite(site) {
		writeErr(w, http.StatusBadRequest, "invalid site name")
		return
	}

	tmpDir, err := os.MkdirTemp(s.DataDir, "deploy-*")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create temp dir")
		return
	}
	defer os.RemoveAll(tmpDir)

	body := http.MaxBytesReader(w, r.Body, maxDeploySize)
	if err := extractTarball(body, tmpDir); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "deploy too large")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	siteDir := filepath.Join(s.DataDir, "sites", site)
	versioned := s.KeepVersions > 0

	asideDir := ""
	if _, err := os.Stat(siteDir); err == nil {
		asideDir, err = s.stashDir(site, versioned)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not replace site")
			return
		}
		if err := os.Rename(siteDir, asideDir); err != nil {
			writeErr(w, http.StatusInternalServerError, "could not replace site")
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusInternalServerError, "could not replace site")
		return
	}

	if err := os.Rename(tmpDir, siteDir); err != nil {
		if asideDir != "" {
			if rerr := os.Rename(asideDir, siteDir); rerr != nil {
				log.Printf("deploy: could not restore %s from %s after failed install: %v", siteDir, asideDir, rerr)
			}
		}
		writeErr(w, http.StatusInternalServerError, "could not install site")
		return
	}

	if asideDir != "" {
		if versioned {
			s.pruneVersions(site)
		} else if err := os.RemoveAll(asideDir); err != nil {
			log.Printf("deploy: could not remove old site dir %s: %v", asideDir, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"site": site,
		"url":  s.siteURL(site),
	})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	if site == "" {
		site = r.Header.Get("X-Shared-Site")
	}
	if !validSite(site) {
		writeErr(w, http.StatusBadRequest, "invalid site name")
		return
	}
	siteDir := filepath.Join(s.DataDir, "sites", site)
	if info, err := os.Stat(siteDir); err != nil || !info.IsDir() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	names, err := listVersions(s.versionsDir(site))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list versions")
		return
	}
	if len(names) == 0 {
		writeErr(w, http.StatusNotFound, "no versions to roll back to")
		return
	}
	newest := filepath.Join(s.versionsDir(site), names[len(names)-1])

	stash, err := s.newVersionDir(site)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not stash current site")
		return
	}
	if err := os.Rename(siteDir, stash); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not stash current site")
		return
	}
	if err := os.Rename(newest, siteDir); err != nil {
		if rerr := os.Rename(stash, siteDir); rerr != nil {
			log.Printf("rollback: could not restore %s from %s: %v", siteDir, stash, rerr)
		}
		writeErr(w, http.StatusInternalServerError, "could not restore version")
		return
	}
	s.pruneVersions(site)

	writeJSON(w, http.StatusOK, map[string]string{
		"site": site,
		"url":  s.siteURL(site),
	})
}

func (s *Server) handleVersions(w http.ResponseWriter, r *http.Request) {
	site := r.URL.Query().Get("site")
	if !validSite(site) {
		writeErr(w, http.StatusBadRequest, "invalid site name")
		return
	}
	names, err := listVersions(s.versionsDir(site))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list versions")
		return
	}
	versions := []map[string]any{}
	for i := len(names) - 1; i >= 0; i-- {
		ts, _ := versionKey(names[i])
		versions = append(versions, map[string]any{"timestamp": ts})
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSite(name) {
		writeErr(w, http.StatusBadRequest, "invalid site name")
		return
	}
	siteDir := filepath.Join(s.DataDir, "sites", name)
	if info, err := os.Stat(siteDir); err != nil || !info.IsDir() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	s.store.DropSite(name)
	for _, p := range []string{
		siteDir,
		filepath.Join(s.DataDir, "db", name),
		filepath.Join(s.DataDir, "uploads", name),
		s.versionsDir(name),
	} {
		if err := os.RemoveAll(p); err != nil {
			log.Printf("delete: could not remove %s: %v", p, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) versionsDir(site string) string {
	return filepath.Join(s.DataDir, "versions", site)
}

func (s *Server) stashDir(site string, versioned bool) (string, error) {
	if versioned {
		return s.newVersionDir(site)
	}
	for {
		p := filepath.Join(s.DataDir, "old-"+strconv.FormatInt(time.Now().UnixNano(), 10))
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		} else if err != nil {
			return "", err
		}
	}
}

func (s *Server) newVersionDir(site string) (string, error) {
	base := s.versionsDir(site)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	p := filepath.Join(base, ts)
	for i := 1; ; i++ {
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		} else if err != nil {
			return "", err
		}
		p = filepath.Join(base, ts+"-"+strconv.Itoa(i))
	}
}

func (s *Server) pruneVersions(site string) {
	names, err := listVersions(s.versionsDir(site))
	if err != nil {
		log.Printf("deploy: could not list versions for %s: %v", site, err)
		return
	}
	if len(names) <= s.KeepVersions {
		return
	}
	for _, name := range names[:len(names)-s.KeepVersions] {
		p := filepath.Join(s.versionsDir(site), name)
		if err := os.RemoveAll(p); err != nil {
			log.Printf("deploy: could not prune version %s: %v", p, err)
		}
	}
}

func listVersions(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return versionLess(names[i], names[j]) })
	return names, nil
}

func versionKey(name string) (int64, string) {
	base := name
	if i := strings.IndexByte(name, '-'); i >= 0 {
		base = name[:i]
	}
	n, _ := strconv.ParseInt(base, 10, 64)
	return n, name
}

func versionLess(a, b string) bool {
	an, _ := versionKey(a)
	bn, _ := versionKey(b)
	if an != bn {
		return an < bn
	}
	return a < b
}

func (s *Server) sweep() {
	sitesDir := filepath.Join(s.DataDir, "sites")
	if entries, err := os.ReadDir(sitesDir); err == nil {
		for _, e := range entries {
			if !strings.Contains(e.Name(), ".old-") {
				continue
			}
			p := filepath.Join(sitesDir, e.Name())
			if err := os.RemoveAll(p); err != nil {
				log.Printf("sweep: could not remove %s: %v", p, err)
			} else {
				log.Printf("sweep: removed orphaned swap dir %s", p)
			}
		}
	}
	if entries, err := os.ReadDir(s.DataDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, "deploy-") && !strings.HasPrefix(name, "old-") {
				continue
			}
			p := filepath.Join(s.DataDir, name)
			if err := os.RemoveAll(p); err != nil {
				log.Printf("sweep: could not remove %s: %v", p, err)
			} else {
				log.Printf("sweep: removed stale temp dir %s", p)
			}
		}
	}
}

func extractTarball(r io.Reader, root string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return errors.New("body is not a gzipped tarball")
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	files := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			if files == 0 {
				return errors.New("tarball contains no files")
			}
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(os.PathSeparator)) || name == ".." {
			return fmt.Errorf("unsafe path in tarball: %s", hdr.Name)
		}
		dst := filepath.Join(root, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size > maxFileSize {
				return fmt.Errorf("file too large in tarball: %s", hdr.Name)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, maxFileSize))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			files++
		default:
			return fmt.Errorf("unsupported entry type in tarball: %s", hdr.Name)
		}
	}
}

func (s *Server) siteURL(site string) string {
	host := site + "." + s.BaseHost
	if _, port, err := net.SplitHostPort(s.Addr); err == nil && port != "" && port != "80" {
		host = net.JoinHostPort(host, port)
	}
	return "http://" + host + "/"
}

func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(filepath.Join(s.DataDir, "sites"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list sites")
		return
	}
	sites := []map[string]any{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		updatedAt := ""
		if info, err := e.Info(); err == nil {
			updatedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		sites = append(sites, map[string]any{
			"name":      e.Name(),
			"updatedAt": updatedAt,
			"bytes":     s.siteBytes(e.Name()),
		})
	}
	sort.Slice(sites, func(i, j int) bool {
		return sites[i]["name"].(string) < sites[j]["name"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"sites": sites})
}

func (s *Server) siteBytes(name string) int64 {
	var total int64
	for _, dir := range []string{
		filepath.Join(s.DataDir, "sites", name),
		filepath.Join(s.DataDir, "db", name),
		filepath.Join(s.DataDir, "uploads", name),
		s.versionsDir(name),
	} {
		filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					total += info.Size()
				}
			}
			return nil
		})
	}
	return total
}
