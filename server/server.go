package main

import (
	"net/http"
	"time"
)

// newServer は HTTP ハンドラが共有する依存関係とデモ用初期データを準備します。
func newServer(cfg config) (*server, error) {
	sm := newStateManager()
	initBytes, err := sm.initJSON()
	if err != nil {
		return nil, err
	}

	return &server{
		cfg:         cfg,
		client:      &http.Client{Timeout: 15 * time.Second},
		states:      map[string]time.Time{},
		sessions:    map[string]tokenResponse{},
		state:       sm,
		initPayload: initBytes,
	}, nil
}

// routes は API エンドポイントをまとめ、最後に CORS ミドルウェアを通します。
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/callback", s.handleCallback)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	return s.withCORS(mux)
}
