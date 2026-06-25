package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

type config struct {
	addr        string
	traqBaseURL string
	clientID    string
	redirectURL string
	scope       string
	appOrigin   string
}

type server struct {
	cfg      config
	client   *http.Client
	mu       sync.Mutex
	states   map[string]time.Time
	sessions map[string]tokenResponse
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}

func main() {
	_ = godotenv.Load()

	cfg := config{
		addr:        getEnv("SERVER_ADDR", ":8080"),
		traqBaseURL: strings.TrimRight(getEnv("TRAQ_BASE_URL", "https://q.trap.jp"), "/"),
		clientID:    os.Getenv("TRAQ_CLIENT_ID"),
		redirectURL: getEnv("TRAQ_REDIRECT_URL", "http://localhost:8080/api/auth/callback"),
		scope:       getEnv("OAUTH_SCOPE", "read"),
		appOrigin:   getEnv("APP_ORIGIN", "http://localhost:5173"),
	}
	if cfg.clientID == "" {
		log.Fatal("TRAQ_CLIENT_ID is required")
	}

	s := &server{
		cfg:      cfg,
		client:   &http.Client{Timeout: 15 * time.Second},
		states:   map[string]time.Time{},
		sessions: map[string]tokenResponse{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/callback", s.handleCallback)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)

	handler := s.withCORS(mux)
	handler = logRequests(handler)
	log.Printf("listening on %s", cfg.addr)
	log.Fatal(http.ListenAndServe(cfg.addr, handler))
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomString(32)
	if err != nil {
		http.Error(w, "failed to create state", http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.states[state] = time.Now().Add(10 * time.Minute)
	s.mu.Unlock()

	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", s.cfg.clientID)
	values.Set("state", state)
	values.Set("scope", s.cfg.scope)

	http.Redirect(w, r, s.cfg.traqBaseURL+"/api/v3/oauth2/authorize?"+values.Encode(), http.StatusFound)
}

func (s *server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	if !s.consumeState(state) {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	token, err := s.exchangeCode(r.Context(), code)
	if err != nil {
		log.Printf("token exchange failed: %v", err)
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}

	sessionID, err := randomString(32)
	if err != nil {
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.sessions[sessionID] = token
	s.mu.Unlock()
	log.Printf("oauth callback succeeded; redirecting to %s", s.cfg.appOrigin)

	http.SetCookie(w, &http.Cookie{
		Name:     "traq_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   max(token.ExpiresIn, 3600),
	})
	http.Redirect(w, r, s.cfg.appOrigin, http.StatusFound)
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	token, ok := s.sessionToken(r)
	if !ok {
		log.Printf("sse rejected: missing or invalid session cookie")
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	messages, errc := s.streamViewStateEvents(ctx, token.AccessToken)
	writeSSE(w, "status", map[string]string{"status": "connected"})
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case msg, ok := <-messages:
			if !ok {
				messages = nil
				continue
			}
			_, _ = fmt.Fprintf(w, "event: USER_VIEWSTATE_CHANGED\ndata: %s\n\n", msg)
			flusher.Flush()
		case err, ok := <-errc:
			if ok && err != nil {
				writeSSE(w, "stream-error", map[string]string{"error": err.Error()})
				flusher.Flush()
			}
			return
		}
	}
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	_, ok := s.sessionToken(r)
	log.Printf("auth check: authenticated=%t", ok)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"authenticated": ok})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("traq_session")
	if err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "traq_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) exchangeCode(ctx context.Context, code string) (tokenResponse, error) {
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("client_id", s.cfg.clientID)
	values.Set("code", code)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.traqBaseURL+"/api/v3/oauth2/token", strings.NewReader(values.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var token tokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return tokenResponse{}, err
	}
	if token.AccessToken == "" {
		return tokenResponse{}, errors.New("token response did not include access_token")
	}
	return token, nil
}

func (s *server) streamViewStateEvents(ctx context.Context, accessToken string) (<-chan json.RawMessage, <-chan error) {
	out := make(chan json.RawMessage)
	errc := make(chan error, 1)

	go func() {
		defer close(out)
		defer close(errc)

		wsURL := s.cfg.traqBaseURL + "/api/v3/ws"
		wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
		wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

		header := http.Header{}
		header.Set("Authorization", "Bearer "+accessToken)
		log.Printf("connecting traQ websocket: %s", wsURL)
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
		if err != nil {
			errc <- fmt.Errorf("websocket dial failed: %w", err)
			return
		}
		log.Printf("traQ websocket connected")
		defer conn.Close()

		go func() {
			<-ctx.Done()
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			_ = conn.Close()
		}()

		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				errc <- fmt.Errorf("websocket read failed: %w", err)
				return
			}

			var event map[string]json.RawMessage
			if err := json.Unmarshal(payload, &event); err != nil {
				continue
			}
			var typ string
			if rawType, ok := event["type"]; ok {
				_ = json.Unmarshal(rawType, &typ)
			}
			if typ == "USER_VIEWSTATE_CHANGED" {
				log.Printf("USER_VIEWSTATE_CHANGED received")
				select {
				case out <- append(json.RawMessage(nil), payload...):
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, errc
}

func (s *server) consumeState(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.states[state]
	delete(s.states, state)
	return ok && time.Now().Before(expiresAt)
}

func (s *server) sessionToken(r *http.Request) (tokenResponse, bool) {
	cookie, err := r.Cookie("traq_session")
	if err != nil {
		return tokenResponse{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.sessions[cookie.Value]
	return token, ok
}

func (s *server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == s.cfg.appOrigin {
			w.Header().Set("Access-Control-Allow-Origin", s.cfg.appOrigin)
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

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s origin=%q", r.Method, r.URL.Path, r.Header.Get("Origin"))
		next.ServeHTTP(w, r)
	})
}

func writeSSE(w io.Writer, event string, value any) {
	payload, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
}

func randomString(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
