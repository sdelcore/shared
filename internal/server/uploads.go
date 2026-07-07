package server

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const maxUploadSize = 32 << 20

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "file"
	}
	return out
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	site := s.siteFromRequest(r)
	if site == "" || !validSite(site) {
		writeErr(w, http.StatusBadRequest, "invalid or missing site")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	url, err := s.saveUpload(site, header.Filename, file)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save file")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"url": url})
}

// saveUpload stores src under the site's uploads dir with a random-prefixed
// name and returns the public /uploads/<site>/<file> URL.
func (s *Server) saveUpload(site, name string, src io.Reader) (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	stored := hex.EncodeToString(buf[:]) + "-" + sanitizeFilename(name)

	dir := filepath.Join(s.DataDir, "uploads", site)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst, err := os.Create(filepath.Join(dir, stored))
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	return "/uploads/" + site + "/" + stored, nil
}

func (s *Server) handleServeUpload(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/uploads/")
	root, err := filepath.Abs(filepath.Join(s.DataDir, "uploads"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	path = filepath.Clean(path)
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	http.ServeFile(w, r, path)
}
