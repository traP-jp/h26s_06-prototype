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
		// ブラウザが送ってきた Origin を見て、開発フロントからの Cookie 付き通信を許可します。
		origin := r.Header.Get("Origin")
		if s.isAllowedOrigin(origin) {
			// 許可する Origin はワイルドカードにせず、実際の Origin を返します。
			w.Header().Set("Access-Control-Allow-Origin", origin)
			// OAuth session Cookie を送るため、credentials を許可します。
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			// フロントが JSON などを送る場合に備えて Content-Type を許可します。
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			// この API で使う GET/POST/OPTIONS だけを許可します。
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		} else if origin != "" {
			// 許可されていない Origin はログに残し、CORS ヘッダを付けません。
			log.Printf("cors origin not allowed: got=%s want=%s", origin, s.cfg.appOrigin)
		}
		// preflight request は本処理へ進めず、204 だけ返します。
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// CORS 処理後、実際の API handler へ渡します。
		next.ServeHTTP(w, r)
	})
}

func (s *server) isAllowedOrigin(origin string) bool {
	// Origin がない curl などは CORS 対象外なので false にします。
	if origin == "" {
		return false
	}
	// 設定された APP_ORIGIN と完全一致する場合は許可します。
	if origin == s.cfg.appOrigin {
		return true
	}

	// APP_ORIGIN を URL として parse します。
	configured, err := url.Parse(s.cfg.appOrigin)
	if err != nil {
		return false
	}
	// リクエスト Origin も URL として parse します。
	requested, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// scheme と port が違う Origin は許可しません。
	if configured.Scheme != requested.Scheme || configured.Port() != requested.Port() {
		return false
	}
	// localhost と 127.0.0.1 の表記揺れだけは開発用に許可します。
	return isLoopbackHost(configured.Hostname()) && isLoopbackHost(requested.Hostname())
}

func isLoopbackHost(host string) bool {
	// 開発環境で使う loopback host のみ true にします。
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API の呼び出し元を追いやすくするため、method/path/origin をログに出します。
		log.Printf("%s %s origin=%q", r.Method, r.URL.Path, r.Header.Get("Origin"))
		// ログ出力後、実際の handler へ処理を渡します。
		next.ServeHTTP(w, r)
	})
}

func writeSSE(w io.Writer, event string, value any) {
	// 任意の Go 値を JSON に変換し、SSE の data として書きます。
	payload, _ := json.Marshal(value)
	// 変換後の JSON bytes を、共通の raw SSE 書き込み関数へ渡します。
	writeRawSSE(w, event, payload)
}

func writeRawSSE(w io.Writer, event string, payload []byte) {
	// EventSource のイベント名として使う event: 行を書きます。
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	// SSE 仕様では data は行ごとに data: prefix を付けるため、改行で分割します。
	for _, line := range strings.Split(string(payload), "\n") {
		_, _ = fmt.Fprintf(w, "data: %s\n", line)
	}
	// 空行で 1 イベントの終端を示します。
	_, _ = fmt.Fprint(w, "\n")
}
