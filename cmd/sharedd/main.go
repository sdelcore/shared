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

	keepVersions := 3
	if v := os.Getenv("SHARED_KEEP_VERSIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Fatalf("invalid SHARED_KEEP_VERSIONS %q", v)
		}
		keepVersions = n
	}

	srv, err := server.New(addr, dataDir, baseHost, keepVersions)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("shared listening on %s (data: %s)", addr, dataDir)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
