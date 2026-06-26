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
	// OAuth クライアント ID がない環境では traQ ログインを開始できないため、明示的に失敗させます。
	if s.cfg.clientID == "" {
		http.Error(w, "TRAQ_CLIENT_ID is not configured", http.StatusServiceUnavailable)
		return
	}

	// state は CSRF 対策として OAuth 認可リクエストと callback を対応付けるランダム文字列です。
	state, err := randomString(32)
	if err != nil {
		http.Error(w, "failed to create state", http.StatusInternalServerError)
		return
	}

	// state は短時間だけ有効にして、古い callback URL の再利用を防ぎます。
	s.mu.Lock()
	s.states[state] = time.Now().Add(10 * time.Minute)
	s.mu.Unlock()

	// traQ の OAuth 認可エンドポイントへ渡すクエリを標準ライブラリで組み立てます。
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", s.cfg.clientID)
	values.Set("state", state)
	values.Set("scope", s.cfg.scope)
	values.Set("redirect_uri", s.cfg.redirectURL)

	// ブラウザを traQ のログイン/認可画面へ遷移させます。
	http.Redirect(w, r, s.cfg.traqBaseURL+"/api/v3/oauth2/authorize?"+values.Encode(), http.StatusFound)
}

func (s *server) handleCallback(w http.ResponseWriter, r *http.Request) {
	// traQ から戻ってきた認可コードと state を取り出します。
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	// どちらかが欠けている callback は OAuth フローとして不正です。
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	// state を一度だけ消費し、ログイン開始リクエストと対応しているか検証します。
	if !s.consumeState(state) {
		http.Error(w, "invalid state", http.StatusBadRequest)
		return
	}

	// 認可コードを traQ の token endpoint に渡し、API 呼び出し用アクセストークンへ交換します。
	token, err := s.exchangeCode(r.Context(), code)
	if err != nil {
		log.Printf("token exchange failed: %v", err)
		http.Error(w, "token exchange failed", http.StatusBadGateway)
		return
	}

	// ブラウザ Cookie に保存するセッション ID をランダムに作ります。
	sessionID, err := randomString(32)
	if err != nil {
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}

	// アクセストークン自体はブラウザへ出さず、サーバ側のメモリにだけ保持します。
	s.mu.Lock()
	s.sessions[sessionID] = token
	s.mu.Unlock()
	log.Printf("oauth callback succeeded; redirecting to %s", s.cfg.appOrigin)

	// HttpOnly Cookie により、フロント JS からセッション ID を直接読めないようにします。
	http.SetCookie(w, &http.Cookie{
		Name:     "traq_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   max(token.ExpiresIn, 3600),
	})
	// 認証後はフロントのトップへ戻し、フロント側が /api/me で認証状態を再確認します。
	http.Redirect(w, r, s.cfg.appOrigin, http.StatusFound)
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	// Cookie からセッションを引けるかだけを確認し、トークン本体は返しません。
	_, ok := s.sessionToken(r)
	log.Printf("auth check: authenticated=%t", ok)
	// フロントのボタン制御に必要な最小限の真偽値だけを返します。
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{
		"authenticated":   ok,
		"oauthConfigured": s.cfg.clientID != "",
	})
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Cookie が存在する場合だけ、対応するサーバ側セッションを削除します。
	cookie, err := r.Cookie("traq_session")
	if err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	// ブラウザ側 Cookie も期限切れにして、次の /api/me で未認証扱いにします。
	http.SetCookie(w, &http.Cookie{Name: "traq_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	// レスポンス本文は不要なので 204 を返します。
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) exchangeCode(ctx context.Context, code string) (tokenResponse, error) {
	// OAuth token endpoint へ送る form body を組み立てます。
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("client_id", s.cfg.clientID)
	values.Set("code", code)
	values.Set("redirect_uri", s.cfg.redirectURL)

	// callback リクエストの context を引き継ぎ、クライアント切断時に token 交換も止まるようにします。
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.traqBaseURL+"/api/v3/oauth2/token", strings.NewReader(values.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	// traQ の token endpoint は application/x-www-form-urlencoded を受け取ります。
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// 共通 HTTP client を使って token endpoint を呼び出します。
	resp, err := s.client.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	// エラー時の本文もログ/エラーへ含めたいので、上限付きで先に読み切ります。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// JSON レスポンスを tokenResponse に変換します。
	var token tokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return tokenResponse{}, err
	}
	// live モードの API 呼び出しには access_token が必須なので、空なら失敗扱いにします。
	if token.AccessToken == "" {
		return tokenResponse{}, errors.New("token response did not include access_token")
	}
	return token, nil
}

func (s *server) consumeState(state string) bool {
	// states map は複数リクエストから触られるため mutex で保護します。
	s.mu.Lock()
	defer s.mu.Unlock()
	// state は一度使ったら成功/失敗に関係なく削除し、リプレイを防ぎます。
	expiresAt, ok := s.states[state]
	delete(s.states, state)
	// 登録済みかつ期限内であれば正しい callback とみなします。
	return ok && time.Now().Before(expiresAt)
}

func (s *server) sessionToken(r *http.Request) (tokenResponse, bool) {
	// セッション ID は traq_session Cookie に入っています。
	cookie, err := r.Cookie("traq_session")
	if err != nil {
		return tokenResponse{}, false
	}
	// sessions map もログイン/ログアウト/SSE で同時に触るため mutex で保護します。
	s.mu.Lock()
	defer s.mu.Unlock()
	// 見つかった token と存在フラグを返し、呼び出し側で認証可否を判断します。
	token, ok := s.sessions[cookie.Value]
	return token, ok
}

func randomString(size int) (string, error) {
	// セッション ID や OAuth state に使うため、暗号学的乱数を使います。
	bytes := make([]byte, size)
	if _, err := crand.Read(bytes); err != nil {
		return "", err
	}
	// URL/Cookie に安全に入れられる base64url 文字列へ変換します。
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
