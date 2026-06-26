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
	return config{
		addr:               getEnv("SERVER_ADDR", ":8080"),
		traqBaseURL:        strings.TrimRight(getEnv("TRAQ_BASE_URL", "https://q.trap.jp"), "/"),
		clientID:           os.Getenv("TRAQ_CLIENT_ID"),
		redirectURL:        getEnv("TRAQ_REDIRECT_URL", "http://localhost:8080/api/auth/callback"),
		scope:              getEnv("OAUTH_SCOPE", "read"),
		appOrigin:          getEnv("APP_ORIGIN", "http://localhost:5173"),
		viewerPollInterval: getEnvDuration("VIEWER_POLL_INTERVAL", 20*time.Second),
		viewerPollChannels: getEnvInt("VIEWER_POLL_CHANNELS", 40),
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		log.Printf("invalid %s=%q; using %s", key, value, fallback)
		return fallback
	}
	return duration
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		log.Printf("invalid %s=%q; using %d", key, value, fallback)
		return fallback
	}
	return parsed
}
