package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"
)

// このファイルは SSE 接続を維持し、demo/live のイベントをフロントへ流します。
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
	var liveChannels []traqChannel
	var liveChannelIDs map[string]bool
	if !demo {
		data, err := s.fetchChannelData(ctx, token.AccessToken)
		if err != nil {
			writeSSE(w, "stream-error", map[string]string{"error": "failed to load traQ channels: " + err.Error()})
			flusher.Flush()
			return
		}
		initPayload = data.InitJSON
		liveChannels = data.Channels
		liveChannelIDs = data.ChannelIDs
	}

	writeRawSSE(w, "init", initPayload)
	writeSSE(w, "status", map[string]string{"status": streamStatus(demo)})
	flusher.Flush()

	var triggers <-chan triggerPayload
	var errc <-chan error
	var viewers <-chan viewerSnapshotPayload
	var poller *viewerPoller
	if demo {
		triggers, errc = s.streamDemoTriggers(ctx)
	} else {
		poller = newViewerPoller(liveChannels, s.cfg.viewerPollChannels)
		triggers, errc = s.streamTraqTriggers(ctx, token.AccessToken)
		viewers = s.streamViewerSnapshots(ctx, token.AccessToken, poller)
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
			if !demo && !isTriggerForActiveChannel(trigger, liveChannelIDs) {
				continue
			}
			var changed bool
			trigger, changed = s.state.applyTrigger(trigger)
			if !changed {
				continue
			}
			if trigger.Type == "msg" && poller != nil {
				poller.noteMessage(trigger.Ch)
			}
			writeSSE(w, "trigger", trigger)
			flusher.Flush()
		case snapshot, ok := <-viewers:
			if !ok {
				viewers = nil
				continue
			}
			writeSSE(w, "viewers", snapshot)
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

func isTriggerForActiveChannel(trigger triggerPayload, channelIDs map[string]bool) bool {
	if len(channelIDs) == 0 {
		return false
	}
	switch trigger.Type {
	case "msg":
		return channelIDs[trigger.Ch]
	case "mov":
		return channelIDs[trigger.To]
	default:
		return false
	}
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
