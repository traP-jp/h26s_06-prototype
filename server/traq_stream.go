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
			channelID := viewState.channelID()
			if viewState.Key == "" || channelID == "" || viewState.State == "none" {
				continue
			}
			triggers = append(triggers, triggerPayload{
				Type: "mov",
				Usr:  hashSessionKey(viewState.Key),
				To:   channelID,
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
