package server

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	oldDir := siteDir + ".old-" + fmt.Sprint(time.Now().UnixNano())
	replaced := false
	if err := os.Rename(siteDir, oldDir); err == nil {
		replaced = true
	} else if !errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusInternalServerError, "could not replace site")
		return
	}
	if err := os.Rename(tmpDir, siteDir); err != nil {
		if replaced {
			os.Rename(oldDir, siteDir)
		}
		writeErr(w, http.StatusInternalServerError, "could not install site")
		return
	}
	if replaced {
		os.RemoveAll(oldDir)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"site": site,
		"url":  s.siteURL(site),
	})
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
	sites := []map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		updatedAt := ""
		if info, err := e.Info(); err == nil {
			updatedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		sites = append(sites, map[string]string{
			"name":      e.Name(),
			"updatedAt": updatedAt,
		})
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i]["name"] < sites[j]["name"] })
	writeJSON(w, http.StatusOK, map[string]any{"sites": sites})
}
