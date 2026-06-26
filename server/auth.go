package main

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// このファイルは traQ OAuth のログイン、コールバック、セッション管理を担当します。
func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.clientID == "" {
		http.Error(w, "TRAQ_CLIENT_ID is not configured", http.StatusServiceUnavailable)
		return
	}

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
	values.Set("redirect_uri", s.cfg.redirectURL)

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

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	_, ok := s.sessionToken(r)
	log.Printf("auth check: authenticated=%t", ok)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{
		"authenticated":   ok,
		"oauthConfigured": s.cfg.clientID != "",
	})
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
	values.Set("redirect_uri", s.cfg.redirectURL)

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

func randomString(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := crand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
