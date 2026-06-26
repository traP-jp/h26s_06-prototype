package main

import (
	"net/http"
	"time"
)

// newServer は HTTP ハンドラが共有する依存関係とデモ用初期データを準備します。
func newServer(cfg config) (*server, error) {
	// demo 用の stateManager を作り、固定チャンネルツリーもここで用意します。
	sm := newStateManager()
	// demo 接続の init SSE で送る JSON を起動時に一度だけ作ります。
	initBytes, err := sm.initJSON()
	if err != nil {
		return nil, err
	}

	// handler method が共有する依存関係とインメモリ状態をまとめた server を返します。
	return &server{
		// cfg は環境変数由来の設定です。
		cfg: cfg,
		// client は traQ API 呼び出し用の HTTP client です。
		client: &http.Client{Timeout: 15 * time.Second},
		// states は OAuth state の一時保存先です。
		states: map[string]time.Time{},
		// sessions は sessionID から tokenResponse を引くメモリストアです。
		sessions: map[string]tokenResponse{},
		// state はチャンネル熱量とユーザー現在地を保持します。
		state: sm,
		// initPayload は demo SSE の初期ツリーとして使います。
		initPayload: initBytes,
	}, nil
}

// routes は API エンドポイントをまとめ、最後に CORS ミドルウェアを通します。
func (s *server) routes() http.Handler {
	// 標準の ServeMux で API path と handler を対応付けます。
	mux := http.NewServeMux()
	// OAuth ログイン開始 endpoint です。
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	// traQ OAuth から戻る callback endpoint です。
	mux.HandleFunc("/api/auth/callback", s.handleCallback)
	// demo/live の SSE endpoint です。
	mux.HandleFunc("/api/events", s.handleEvents)
	// フロントが認証状態を確認する endpoint です。
	mux.HandleFunc("/api/me", s.handleMe)
	// セッション破棄 endpoint です。
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	// フロント dev server から Cookie 付きで呼べるよう CORS を適用します。
	return s.withCORS(mux)
}
