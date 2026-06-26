package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// このファイルは CORS、リクエストログ、SSE 書き込みなどの HTTP 周辺処理をまとめます。
func (s *server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if s.isAllowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		} else if origin != "" {
			log.Printf("cors origin not allowed: got=%s want=%s", origin, s.cfg.appOrigin)
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) isAllowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	if origin == s.cfg.appOrigin {
		return true
	}

	configured, err := url.Parse(s.cfg.appOrigin)
	if err != nil {
		return false
	}
	requested, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if configured.Scheme != requested.Scheme || configured.Port() != requested.Port() {
		return false
	}
	return isLoopbackHost(configured.Hostname()) && isLoopbackHost(requested.Hostname())
}

func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s origin=%q", r.Method, r.URL.Path, r.Header.Get("Origin"))
		next.ServeHTTP(w, r)
	})
}

func writeSSE(w io.Writer, event string, value any) {
	payload, _ := json.Marshal(value)
	writeRawSSE(w, event, payload)
}

func writeRawSSE(w io.Writer, event string, payload []byte) {
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	for _, line := range strings.Split(string(payload), "\n") {
		_, _ = fmt.Fprintf(w, "data: %s\n", line)
	}
	_, _ = fmt.Fprint(w, "\n")
}
