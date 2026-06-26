package main

import (
	"log"
	"net/http"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	cfg := loadConfig()
	s, err := newServer(cfg)
	if err != nil {
		log.Fatalf("failed to initialize server: %v", err)
	}

	handler := logRequests(s.routes())
	log.Printf("listening on %s", cfg.addr)
	if cfg.clientID == "" {
		log.Printf("TRAQ_CLIENT_ID is not set; OAuth login is disabled, but demo SSE is available")
	}
	log.Fatal(http.ListenAndServe(cfg.addr, handler))
}
