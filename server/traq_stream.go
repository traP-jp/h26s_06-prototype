package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// このファイルは traQ WebSocket を読み、画面で扱いやすい trigger に正規化します。
func (s *server) streamTraqTriggers(ctx context.Context, accessToken string) (<-chan triggerPayload, <-chan error) {
	// out は handleEvents に渡す正規化済み trigger のストリームです。
	out := make(chan triggerPayload)
	// errc は WebSocket 接続/読み取りの致命的エラーだけを返します。
	errc := make(chan error, 1)

	go func() {
		// 呼び出し側が range/select で終了を検知できるよう、goroutine 終了時に閉じます。
		defer close(out)
		defer close(errc)

		// REST API の base URL から WebSocket URL を作ります。
		wsURL := s.cfg.traqBaseURL + "/api/v3/ws"
		// https の環境では wss、http の環境では ws に置き換えます。
		wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
		wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

		// traQ WebSocket も Bearer token で認証します。
		header := http.Header{}
		header.Set("Authorization", "Bearer "+accessToken)
		log.Printf("connecting traQ websocket: %s", wsURL)
		// ctx を渡すことで SSE 切断時に dial 中でもキャンセルできます。
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
		if err != nil {
			errc <- fmt.Errorf("websocket dial failed: %w", err)
			return
		}
		log.Printf("traQ websocket connected")
		defer conn.Close()

		// timeline_streaming を有効にし、MESSAGE_CREATED などを流してもらいます。
		if err := conn.WriteMessage(websocket.TextMessage, []byte("timeline_streaming:on")); err != nil {
			errc <- fmt.Errorf("websocket command failed: %w", err)
			return
		}

		// ctx がキャンセルされたら close frame を送り、ReadMessage のブロックを解除します。
		go func() {
			<-ctx.Done()
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			_ = conn.Close()
		}()

		// WebSocket が閉じるまで JSON イベントを読み続けます。
		for {
			// payload は traQ WebSocket の 1 イベント分の JSON です。
			_, payload, err := conn.ReadMessage()
			if err != nil {
				// SSE 側の正常終了ならエラーとして UI に出さず、そのまま終わります。
				if ctx.Err() != nil {
					return
				}
				// 予期しない read 失敗は live stream の致命的エラーとして返します。
				errc <- fmt.Errorf("websocket read failed: %w", err)
				return
			}

			// traQ 固有の event body を、フロント共通の msg/mov trigger へ変換します。
			triggers, err := s.parseTraqTriggers(ctx, accessToken, payload)
			if err != nil {
				// 1 イベントの変換失敗で全体を止めず、ログに残して次のイベントを読みます。
				log.Printf("traQ websocket event skipped: %v", err)
				continue
			}
			for _, trigger := range triggers {
				select {
				// 正規化した trigger を handleEvents へ渡します。
				case out <- trigger:
				// SSE が切断されたら送信待ちをやめて goroutine を終了します。
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, errc
}

func (s *server) parseTraqTriggers(ctx context.Context, accessToken string, payload []byte) ([]triggerPayload, error) {
	// まず type と body を持つ共通 envelope として読みます。
	var event wsEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, err
	}

	// type は大文字で比較し、traQ 側の表記揺れに少し強くします。
	normalizedType := strings.ToUpper(event.Type)
	switch normalizedType {
	case "MESSAGE_CREATED":
		// メッセージ作成イベントは message ID だけを持つため、チャンネル ID は追加 API で取得します。
		var body wsMessageCreatedBody
		if err := json.Unmarshal(event.Body, &body); err != nil {
			return nil, err
		}
		// ID が空なら可視化対象にできないので、何も返しません。
		if body.ID == "" {
			return nil, nil
		}
		// メッセージ詳細を取得し、どのチャンネルで発生したかを調べます。
		channelID, err := s.fetchMessageChannelID(ctx, accessToken, body.ID)
		if err != nil {
			return nil, err
		}
		// 削除済みなどでチャンネルが分からない場合はスキップします。
		if channelID == "" {
			return nil, nil
		}
		// フロントでは msg trigger として、対象チャンネルを強く光らせます。
		return []triggerPayload{{Type: "msg", Ch: channelID}}, nil
	case "USER_VIEWSTATE_CHANGED":
		// viewstate はユーザーの閲覧/編集中チャンネル変化を含みます。
		var body wsUserViewStateChangedBody

		if err := json.Unmarshal(event.Body, &body); err != nil {
			return nil, err
		}
		// 開発中に実際の viewstate 形状を確認するため、受信内容をログへ出します。
		log.Printf("%v", body)
		// 1 つの WebSocket イベントに複数 viewstate が入ることがあるため slice で返します。
		triggers := make([]triggerPayload, 0, len(body.ViewStates))
		for _, viewState := range body.ViewStates {
			// channelId / channel_id の両対応を channelID メソッドへ寄せています。
			channelID := viewState.channelID()
			// key/channel が空、または state=none の行は可視化しても意味が薄いためスキップします。
			if viewState.Key == "" || channelID == "" || viewState.State == "none" {
				continue
			}
			// セッションキーは生で出さず、短い匿名 ID へ変換して mov trigger にします。
			triggers = append(triggers, triggerPayload{
				Type: "mov",
				Usr:  hashSessionKey(viewState.Key),
				To:   channelID,
			})
		}
		// mov trigger の from は stateManager.applyTrigger で前回位置から補完します。
		return triggers, nil
	default:
		// その他の traQ WebSocket イベントは今回の可視化対象外です。
		return nil, nil
	}
}

func (s *server) fetchMessageChannelID(ctx context.Context, accessToken string, messageID string) (string, error) {
	// messageID はパス要素なので PathEscape して URL に埋め込みます。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.traqBaseURL+"/api/v3/messages/"+url.PathEscape(messageID), nil)
	if err != nil {
		return "", err
	}
	// メッセージ詳細 API も live 接続の Bearer token で認証します。
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// 共通 HTTP client で traQ API を呼びます。
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// エラー時の本文を含めるため、上限付きで読み込みます。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// メッセージが見つからない場合は、イベントを単にスキップできるよう空文字にします。
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	// その他の非 2xx は API エラーとして呼び出し元へ返します。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("message endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// channelId だけが必要なので、必要最小限の構造体へ unmarshl します。
	var message traqMessage
	if err := json.Unmarshal(body, &message); err != nil {
		return "", err
	}
	// MESSAGE_CREATED をフロントの msg trigger に変換するための channel ID を返します。
	return message.ChannelID, nil
}

func hashSessionKey(sessionKey string) string {
	// viewstate の key をそのままログ/UI に出さないため SHA-256 で匿名化します。
	sum := sha256.Sum256([]byte(sessionKey))
	// UI で扱いやすいよう、先頭 12 桁だけを session_ prefix 付きで使います。
	return "session_" + hex.EncodeToString(sum[:])[:12]
}
