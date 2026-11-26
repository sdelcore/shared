package main

import (
	"log"
	"os"
	"strconv"

	"github.com/sdelcore/shared/internal/server"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := envOr("SHARED_ADDR", ":8787")
	dataDir := envOr("SHARED_DATA", "./data")
	// Left empty on purpose: with a public URL configured the public host is
	// the sensible base host, and server.New applies that (and "localhost"
	// when neither is set).
	baseHost := os.Getenv("SHARED_BASE_HOST")
	urlMode := envOr("SHARED_URL_MODE", server.URLModeSubdomain)
	pathPrefix := envOr("SHARED_PATH_PREFIX", server.DefaultPathPrefix)
	publicURL := os.Getenv("SHARED_PUBLIC_URL")

	keepVersions := 3
	if v := os.Getenv("SHARED_KEEP_VERSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Fatalf("invalid SHARED_KEEP_VERSIONS %q", v)
		}
		keepVersions = n
	}

	srv, err := server.New(addr, dataDir, baseHost, keepVersions, urlMode, pathPrefix, publicURL)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("shared listening on %s (data: %s, url mode: %s)", addr, dataDir, srv.URLMode)
	if srv.PublicURL != "" {
		log.Printf("sites are published at %s", srv.PublicURL)
	}
	if srv.URLMode == server.URLModePath {
		log.Printf("sites are served under the path prefix %s", srv.PathPrefix)
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
