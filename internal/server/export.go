package server

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="shared-export.tar.gz"`)

	gw := gzip.NewWriter(w)
	tw := tar.NewWriter(gw)

	root := filepath.Clean(s.DataDir)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		if skipExport(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		log.Printf("export: %v", err)
	}
	if err := tw.Close(); err != nil {
		log.Printf("export: closing tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		log.Printf("export: closing gzip: %v", err)
	}
}

func skipExport(rel string) bool {
	first := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
	}
	if strings.HasPrefix(first, "deploy-") || strings.HasPrefix(first, "old-") {
		return true
	}
	base := filepath.Base(rel)
	return strings.Contains(base, ".old-")
}
