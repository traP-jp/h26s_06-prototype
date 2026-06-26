package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// loadConfig は環境変数からアプリ設定を読み込み、未指定時は開発用の既定値を使います。
func loadConfig() config {
	// config は server 起動時に一度だけ読み、各 handler から共有します。
	return config{
		// SERVER_ADDR は Go HTTP server が listen するアドレスです。
		addr: getEnv("SERVER_ADDR", ":8080"),
		// TRAQ_BASE_URL は末尾 slash を落とし、API path と安全に連結できる形にします。
		traqBaseURL: strings.TrimRight(getEnv("TRAQ_BASE_URL", "https://q.trap.jp"), "/"),
		// TRAQ_CLIENT_ID は OAuth login に必須で、未設定なら demo のみ動きます。
		clientID: os.Getenv("TRAQ_CLIENT_ID"),
		// TRAQ_REDIRECT_URL は traQ OAuth client に登録した callback URL と一致させる必要があります。
		redirectURL: getEnv("TRAQ_REDIRECT_URL", "http://localhost:8080/api/auth/callback"),
		// OAUTH_SCOPE は traQ API を読むための OAuth scope です。
		scope: getEnv("OAUTH_SCOPE", "read"),
		// APP_ORIGIN は CORS 許可と OAuth 後のリダイレクト先に使います。
		appOrigin: getEnv("APP_ORIGIN", "http://localhost:5173"),
		// VIEWER_POLL_INTERVAL は live viewer API のポーリング間隔です。
		viewerPollInterval: getEnvDuration("VIEWER_POLL_INTERVAL", 20*time.Second),
		// VIEWER_POLL_CHANNELS は 1 tick あたりに viewers API を叩くチャンネル数です。
		viewerPollChannels: getEnvInt("VIEWER_POLL_CHANNELS", 40),
	}
}

func getEnv(key, fallback string) string {
	// 環境変数が空でなければそれを優先します。
	if value := os.Getenv(key); value != "" {
		return value
	}
	// 未設定時は開発環境で動く fallback を使います。
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	// duration 文字列を読むため、まず生の環境変数を取得します。
	value := os.Getenv(key)
	if value == "" {
		// 未設定なら fallback をそのまま使います。
		return fallback
	}
	// Go の time.ParseDuration 形式、例: 20s / 1m を受け付けます。
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		// 不正値はログに残し、起動自体は止めず fallback へ戻します。
		log.Printf("invalid %s=%q; using %s", key, value, fallback)
		return fallback
	}
	// 正常に parse できた正の duration を返します。
	return duration
}

func getEnvInt(key string, fallback int) int {
	// 整数設定用に環境変数を取得します。
	value := os.Getenv(key)
	if value == "" {
		// 未設定時は fallback を使います。
		return fallback
	}
	// 文字列を int に変換します。
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		// 0 以下や parse 失敗は設定ミスとしてログに残し、fallback を使います。
		log.Printf("invalid %s=%q; using %d", key, value, fallback)
		return fallback
	}
	// 正常な正の整数を返します。
	return parsed
}
