package main

import (
	"log"
	"net/http"

	"github.com/joho/godotenv"
)

func main() {
	// .env があれば読み込み、ローカル開発で環境変数を設定しやすくします。
	_ = godotenv.Load()

	// 環境変数から server 設定を構築します。
	cfg := loadConfig()
	// handler が共有する HTTP client、セッション、デモ初期データを作ります。
	s, err := newServer(cfg)
	if err != nil {
		log.Fatalf("failed to initialize server: %v", err)
	}

	// API routes にアクセスログ middleware を巻きます。
	handler := logRequests(s.routes())
	// listen 先をログへ出し、起動確認しやすくします。
	log.Printf("listening on %s", cfg.addr)
	if cfg.clientID == "" {
		// client ID がなくても demo SSE は使えるため、警告ログに留めます。
		log.Printf("TRAQ_CLIENT_ID is not set; OAuth login is disabled, but demo SSE is available")
	}
	// HTTP server を起動し、致命的エラーがあればプロセスを終了します。
	log.Fatal(http.ListenAndServe(cfg.addr, handler))
}
