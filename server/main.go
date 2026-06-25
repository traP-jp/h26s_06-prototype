package main

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

const grandRootID = "grand_root"

type config struct {
	addr        string
	traqBaseURL string
	clientID    string
	redirectURL string
	scope       string
	appOrigin   string
}

type server struct {
	cfg         config
	client      *http.Client
	mu          sync.Mutex
	states      map[string]time.Time
	sessions    map[string]tokenResponse
	state       *stateManager
	initPayload []byte
}

type channel struct {
	ID            string
	Name          string
	ParentID      string
	Children      []string
	IslandID      int
	Depth         int
	Score         float64
	LastSyncScore float64
	LastSyncTime  time.Time
}

type userState struct {
	UserID         string
	CurrentChannel string
	LastUpdated    time.Time
}

type stateManager struct {
	mu       sync.RWMutex
	channels map[string]*channel
	users    map[string]*userState
}

type initChannel struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	ParentID string   `json:"parentId"`
	Children []string `json:"children"`
	IslandID int      `json:"islandId"`
	Depth    int      `json:"depth"`
}

type initPayload struct {
	Channels map[string]initChannel `json:"channels"`
}

type traqChannelList struct {
	Public []traqChannel `json:"public"`
}

type traqChannel struct {
	ID       string   `json:"id"`
	ParentID *string  `json:"parentId"`
	Archived bool     `json:"archived"`
	Name     string   `json:"name"`
	Children []string `json:"children"`
}

type triggerPayload struct {
	Type string `json:"type"`
	Ch   string `json:"ch,omitempty"`
	Usr  string `json:"usr,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type syncPayload struct {
	TS     int64              `json:"ts"`
	Deltas map[string]float64 `json:"deltas"`
}

type wsEvent struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

type wsMessageCreatedBody struct {
	ID       string `json:"id"`
	IsCiting bool   `json:"is_citing"`
}

type wsUserViewStateChangedBody struct {
	ViewStates []wsViewState `json:"view_states"`
}

type wsViewState struct {
	Key       string `json:"key"`
	ChannelID string `json:"channelId"`
	State     string `json:"state"`
}

type traqMessage struct {
	ChannelID string `json:"channelId"`
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

	sm := newStateManager()
	initBytes, err := sm.initJSON()
	if err != nil {
		log.Fatalf("failed to build init payload: %v", err)
	}

	s := &server{
		cfg:         cfg,
		client:      &http.Client{Timeout: 15 * time.Second},
		states:      map[string]time.Time{},
		sessions:    map[string]tokenResponse{},
		state:       sm,
		initPayload: initBytes,
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
	if cfg.clientID == "" {
		log.Printf("TRAQ_CLIENT_ID is not set; OAuth login is disabled, but demo SSE is available")
	}
	log.Fatal(http.ListenAndServe(cfg.addr, handler))
}

func newStateManager() *stateManager {
	now := time.Now()
	channels := map[string]*channel{
		grandRootID: {
			ID:           grandRootID,
			Name:         "Grand Root",
			ParentID:     "",
			IslandID:     -1,
			Depth:        0,
			LastSyncTime: now,
		},
	}

	rootNames := []string{"general", "random", "event", "team", "times", "project", "creative", "tech", "archive"}
	for i, name := range rootNames {
		rootID := fmt.Sprintf("island-%d", i+1)
		channels[grandRootID].Children = append(channels[grandRootID].Children, rootID)
		channels[rootID] = &channel{
			ID:           rootID,
			Name:         name,
			ParentID:     grandRootID,
			IslandID:     i,
			Depth:        1,
			LastSyncTime: now,
		}

		for j := 0; j < 5; j++ {
			childID := fmt.Sprintf("%s-ch-%d", rootID, j+1)
			channels[rootID].Children = append(channels[rootID].Children, childID)
			channels[childID] = &channel{
				ID:           childID,
				Name:         fmt.Sprintf("%s/%02d", name, j+1),
				ParentID:     rootID,
				IslandID:     i,
				Depth:        2,
				LastSyncTime: now,
			}

			for k := 0; k < 2; k++ {
				leafID := fmt.Sprintf("%s-sub-%d", childID, k+1)
				channels[childID].Children = append(channels[childID].Children, leafID)
				channels[leafID] = &channel{
					ID:           leafID,
					Name:         fmt.Sprintf("%s/%02d/%02d", name, j+1, k+1),
					ParentID:     childID,
					IslandID:     i,
					Depth:        3,
					LastSyncTime: now,
				}
			}
		}
	}

	return &stateManager{
		channels: channels,
		users:    map[string]*userState{},
	}
}

func (sm *stateManager) initJSON() ([]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	payload := initPayload{Channels: make(map[string]initChannel, len(sm.channels))}
	for id, ch := range sm.channels {
		payload.Channels[id] = initChannel{
			ID:       ch.ID,
			Name:     ch.Name,
			ParentID: ch.ParentID,
			Children: append([]string(nil), ch.Children...),
			IslandID: ch.IslandID,
			Depth:    ch.Depth,
		}
	}
	return json.Marshal(payload)
}

func (sm *stateManager) applyTrigger(trigger triggerPayload) (triggerPayload, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch trigger.Type {
	case "msg":
		sm.addScoreLocked(trigger.Ch, 46)
	case "mov":
		if trigger.Usr != "" && trigger.To != "" {
			user, ok := sm.users[trigger.Usr]
			if !ok {
				user = &userState{UserID: trigger.Usr}
				sm.users[trigger.Usr] = user
			}
			if trigger.From == "" {
				trigger.From = user.CurrentChannel
			}
			if trigger.From == trigger.To {
				return trigger, false
			}
			user.CurrentChannel = trigger.To
			user.LastUpdated = time.Now()
		}
		sm.addScoreLocked(trigger.To, 11)
	}
	return trigger, true
}

func (sm *stateManager) addScoreLocked(channelID string, amount float64) {
	for depth := 0; channelID != ""; depth++ {
		ch, ok := sm.channels[channelID]
		if !ok {
			return
		}
		ch.Score = math.Min(100, ch.Score+amount*math.Pow(0.45, float64(depth)))
		channelID = ch.ParentID
	}
}

func (sm *stateManager) syncPayload() syncPayload {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	deltas := make(map[string]float64)
	for _, ch := range sm.channels {
		elapsed := now.Sub(ch.LastSyncTime).Seconds()
		ch.Score *= math.Exp(-elapsed / 24)
		if math.Abs(ch.Score-ch.LastSyncScore) >= 0.35 || (ch.Score > 0.1 && elapsed >= 8) {
			deltas[ch.ID] = math.Round(ch.Score*10) / 10
			ch.LastSyncScore = ch.Score
			ch.LastSyncTime = now
		}
	}
	return syncPayload{TS: now.Unix(), Deltas: deltas}
}

func (sm *stateManager) randomChannelID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	candidates := make([]string, 0, len(sm.channels))
	for id, ch := range sm.channels {
		if id != grandRootID && len(ch.Children) == 0 {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return grandRootID
	}
	return candidates[rand.Intn(len(candidates))]
}

func (s *server) fetchChannelInitPayload(ctx context.Context, accessToken string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.traqBaseURL+"/api/v3/channels", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("channels endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var channels traqChannelList
	if err := json.Unmarshal(body, &channels); err != nil {
		return nil, err
	}
	return buildTraqInitPayload(channels.Public)
}

func buildTraqInitPayload(channels []traqChannel) ([]byte, error) {
	payload := initPayload{Channels: map[string]initChannel{
		grandRootID: {
			ID:       grandRootID,
			Name:     "Grand Root",
			ParentID: "",
			Children: []string{},
			IslandID: -1,
			Depth:    0,
		},
	}}

	included := make(map[string]traqChannel, len(channels))
	for _, ch := range channels {
		if ch.ID == "" || ch.Archived {
			continue
		}
		included[ch.ID] = ch
	}

	for _, ch := range channels {
		if _, ok := included[ch.ID]; !ok {
			continue
		}
		parentID := grandRootID
		if ch.ParentID != nil {
			if _, ok := included[*ch.ParentID]; ok {
				parentID = *ch.ParentID
			}
		}
		payload.Channels[ch.ID] = initChannel{
			ID:       ch.ID,
			Name:     ch.Name,
			ParentID: parentID,
			Children: []string{},
		}
	}

	for _, ch := range channels {
		if _, ok := payload.Channels[ch.ID]; !ok {
			continue
		}
		parentID := payload.Channels[ch.ID].ParentID
		parent := payload.Channels[parentID]
		parent.Children = append(parent.Children, ch.ID)
		payload.Channels[parentID] = parent
	}

	for islandID, rootID := range payload.Channels[grandRootID].Children {
		assignInitChannelLayout(payload.Channels, rootID, islandID, 1)
	}

	return json.Marshal(payload)
}

func assignInitChannelLayout(channels map[string]initChannel, id string, islandID int, depth int) {
	ch, ok := channels[id]
	if !ok {
		return
	}
	ch.IslandID = islandID
	ch.Depth = depth
	channels[id] = ch
	for _, childID := range ch.Children {
		assignInitChannelLayout(channels, childID, islandID, depth+1)
	}
}

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

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	demo := r.URL.Query().Get("demo") == "1"
	var token tokenResponse
	if !demo {
		var ok bool
		token, ok = s.sessionToken(r)
		if !ok {
			log.Printf("sse rejected: missing or invalid session cookie")
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}
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

	initPayload := s.initPayload
	if !demo {
		var err error
		initPayload, err = s.fetchChannelInitPayload(ctx, token.AccessToken)
		if err != nil {
			writeSSE(w, "stream-error", map[string]string{"error": "failed to load traQ channels: " + err.Error()})
			flusher.Flush()
			return
		}
	}

	writeRawSSE(w, "init", initPayload)
	writeSSE(w, "status", map[string]string{"status": streamStatus(demo)})
	flusher.Flush()

	var triggers <-chan triggerPayload
	var errc <-chan error
	if demo {
		triggers, errc = s.streamDemoTriggers(ctx)
	} else {
		triggers, errc = s.streamTraqTriggers(ctx, token.AccessToken)
	}

	syncTicker := time.NewTicker(8 * time.Second)
	heartbeat := time.NewTicker(25 * time.Second)
	defer syncTicker.Stop()
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-syncTicker.C:
			payload := s.state.syncPayload()
			if len(payload.Deltas) > 0 {
				writeSSE(w, "sync", payload)
				flusher.Flush()
			}
		case trigger, ok := <-triggers:
			if !ok {
				triggers = nil
				continue
			}
			var changed bool
			trigger, changed = s.state.applyTrigger(trigger)
			if !changed {
				continue
			}
			writeSSE(w, "trigger", trigger)
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

func streamStatus(demo bool) string {
	if demo {
		return "demo connected"
	}
	return "traQ connected"
}

func (s *server) streamDemoTriggers(ctx context.Context) (<-chan triggerPayload, <-chan error) {
	out := make(chan triggerPayload)
	errc := make(chan error)

	go func() {
		defer close(out)
		defer close(errc)

		ticker := time.NewTicker(900 * time.Millisecond)
		defer ticker.Stop()
		userChannels := map[string]string{}
		users := []string{"demo-user-a", "demo-user-b", "demo-user-c", "demo-user-d"}
		count := 0

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				to := s.state.randomChannelID()
				if count%3 == 0 {
					out <- triggerPayload{Type: "msg", Ch: to}
				} else {
					usr := users[rand.Intn(len(users))]
					out <- triggerPayload{Type: "mov", Usr: usr, From: userChannels[usr], To: to}
					userChannels[usr] = to
				}
				count++
			}
		}
	}()

	return out, errc
}

func (s *server) streamTraqTriggers(ctx context.Context, accessToken string) (<-chan triggerPayload, <-chan error) {
	out := make(chan triggerPayload)
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

		if err := conn.WriteMessage(websocket.TextMessage, []byte("timeline_streaming:on")); err != nil {
			errc <- fmt.Errorf("websocket command failed: %w", err)
			return
		}

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

			triggers, err := s.parseTraqTriggers(ctx, accessToken, payload)
			if err != nil {
				log.Printf("traQ websocket event skipped: %v", err)
				continue
			}
			for _, trigger := range triggers {
				select {
				case out <- trigger:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, errc
}

func (s *server) parseTraqTriggers(ctx context.Context, accessToken string, payload []byte) ([]triggerPayload, error) {
	var event wsEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, err
	}

	normalizedType := strings.ToUpper(event.Type)
	switch normalizedType {
	case "MESSAGE_CREATED":
		var body wsMessageCreatedBody
		if err := json.Unmarshal(event.Body, &body); err != nil {
			return nil, err
		}
		if body.ID == "" {
			return nil, nil
		}
		channelID, err := s.fetchMessageChannelID(ctx, accessToken, body.ID)
		if err != nil {
			return nil, err
		}
		if channelID == "" {
			return nil, nil
		}
		return []triggerPayload{{Type: "msg", Ch: channelID}}, nil
	case "USER_VIEWSTATE_CHANGED":
		var body wsUserViewStateChangedBody

		if err := json.Unmarshal(event.Body, &body); err != nil {
			return nil, err
		}
		log.Printf("%v", body)
		triggers := make([]triggerPayload, 0, len(body.ViewStates))
		for _, viewState := range body.ViewStates {
			if viewState.Key == "" || viewState.ChannelID == "" || viewState.State == "none" {
				continue
			}
			triggers = append(triggers, triggerPayload{
				Type: "mov",
				Usr:  hashSessionKey(viewState.Key),
				To:   viewState.ChannelID,
			})
		}
		return triggers, nil
	default:
		return nil, nil
	}
}

func (s *server) fetchMessageChannelID(ctx context.Context, accessToken string, messageID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.traqBaseURL+"/api/v3/messages/"+url.PathEscape(messageID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("message endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var message traqMessage
	if err := json.Unmarshal(body, &message); err != nil {
		return "", err
	}
	return message.ChannelID, nil
}

func hashSessionKey(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return "session_" + hex.EncodeToString(sum[:])[:12]
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
	writeRawSSE(w, event, payload)
}

func writeRawSSE(w io.Writer, event string, payload []byte) {
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	for _, line := range strings.Split(string(payload), "\n") {
		_, _ = fmt.Fprintf(w, "data: %s\n", line)
	}
	_, _ = fmt.Fprint(w, "\n")
}

func randomString(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := crand.Read(bytes); err != nil {
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
