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
	baseHost := envOr("SHARED_BASE_HOST", "localhost")
	urlMode := envOr("SHARED_URL_MODE", server.URLModeSubdomain)
	pathPrefix := envOr("SHARED_PATH_PREFIX", server.DefaultPathPrefix)

	keepVersions := 3
	if v := os.Getenv("SHARED_KEEP_VERSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Fatalf("invalid SHARED_KEEP_VERSIONS %q", v)
		}
		keepVersions = n
	}

	srv, err := server.New(addr, dataDir, baseHost, keepVersions, urlMode, pathPrefix)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("shared listening on %s (data: %s, url mode: %s)", addr, dataDir, srv.URLMode)
	if srv.URLMode == server.URLModePath {
		log.Printf("sites are served under the path prefix %s", srv.PathPrefix)
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
