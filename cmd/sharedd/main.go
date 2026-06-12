package main

import (
	"log"
	"os"

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

	srv, err := server.New(addr, dataDir, baseHost)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("shared listening on %s (data: %s)", addr, dataDir)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
