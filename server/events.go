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
	// demo=1 のときは traQ 認証を使わず、サーバ内で生成した擬似イベントを流します。
	demo := r.URL.Query().Get("demo") == "1"
	// live モードでは、この後でセッション Cookie から traQ のアクセストークンを取り出します。
	var token tokenResponse
	if !demo {
		// セッションがない live 接続は traQ API を呼べないため、ここで拒否します。
		var ok bool
		token, ok = s.sessionToken(r)
		if !ok {
			log.Printf("sse rejected: missing or invalid session cookie")
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}
	}

	// SSE はレスポンスを逐次 flush する必要があるので、ResponseWriter が対応しているか確認します。
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// ブラウザに EventSource として扱わせ、プロキシにバッファリングされにくいヘッダを設定します。
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// HTTP リクエストが閉じたら、WebSocket や viewer poller もまとめて止まるようにします。
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// demo では起動時に作った固定ツリー、live では traQ から取ったチャンネルツリーを使います。
	initPayload := s.initPayload
	var liveChannels []traqChannel
	var liveChannelIDs map[string]bool
	if !demo {
		// live 接続開始時にチャンネル一覧を読み、アーカイブ済みチャンネルを除いた可視化データを作ります。
		data, err := s.fetchChannelData(ctx, token.AccessToken)
		if err != nil {
			// 初期ツリーを返せないと画面を作れないため、SSE 上のエラーイベントを返して終了します。
			writeSSE(w, "stream-error", map[string]string{"error": "failed to load traQ channels: " + err.Error()})
			flusher.Flush()
			return
		}
		initPayload = data.InitJSON
		liveChannels = data.Channels
		liveChannelIDs = data.ChannelIDs
	}

	// 最初にチャンネルツリーを送り、フロントの Three.js シーンを構築できる状態にします。
	writeRawSSE(w, "init", initPayload)
	// 現在どちらのストリームに接続したかを UI のステータス表示へ伝えます。
	writeSSE(w, "status", map[string]string{"status": streamStatus(demo)})
	flusher.Flush()

	// triggers は msg/mov の可視化イベント、viewers は live 専用の閲覧者スナップショットです。
	var triggers <-chan triggerPayload
	var errc <-chan error
	var viewers <-chan viewerSnapshotPayload
	var poller *viewerPoller
	if demo {
		// demo はサーバ内で定期的に msg/mov を作るだけなので、traQ への接続は不要です。
		triggers, errc = s.streamDemoTriggers(ctx)
	} else {
		// viewer poller はメッセージが来たチャンネルを優先してサンプリングします。
		poller = newViewerPoller(liveChannels, s.cfg.viewerPollChannels)
		// traQ WebSocket から MESSAGE_CREATED / USER_VIEWSTATE_CHANGED を読みます。
		triggers, errc = s.streamTraqTriggers(ctx, token.AccessToken)
		// viewer API は WebSocket には含まれないため、別 goroutine で周期的に取得します。
		viewers = s.streamViewerSnapshots(ctx, token.AccessToken, poller)
	}

	// syncTicker はサーバ内の熱量減衰をフロントへ同期するための周期です。
	syncTicker := time.NewTicker(8 * time.Second)
	// heartbeat は長時間イベントがないときでも SSE 接続を維持するためのコメント行を送ります。
	heartbeat := time.NewTicker(25 * time.Second)
	defer syncTicker.Stop()
	defer heartbeat.Stop()

	// ここからは SSE 接続が切れるまで、複数の入力元を select で待ち続けます。
	for {
		select {
		case <-r.Context().Done():
			// ブラウザが EventSource を閉じたら、上の defer cancel によって下流 goroutine も止まります。
			return
		case <-heartbeat.C:
			// SSE のコメント行はフロントにはイベントとして届かず、接続維持だけに使えます。
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-syncTicker.C:
			// サーバ側で持っている各チャンネルの熱量差分をまとめて計算します。
			payload := s.state.syncPayload()
			if len(payload.Deltas) > 0 {
				// 差分があるときだけ送ることで、無駄な SSE イベントを減らします。
				writeSSE(w, "sync", payload)
				flusher.Flush()
			}
		case trigger, ok := <-triggers:
			if !ok {
				// チャンネルが閉じたら nil にして、以後この case が選ばれないようにします。
				triggers = nil
				continue
			}
			if !demo && !isTriggerForActiveChannel(trigger, liveChannelIDs) {
				// live ではアーカイブ済み/未知チャンネルのイベントを画面に出さないようにします。
				continue
			}
			// msg/mov をサーバ内状態に反映し、移動なしなど表示不要なイベントを落とします。
			var changed bool
			trigger, changed = s.state.applyTrigger(trigger)
			if !changed {
				continue
			}
			if trigger.Type == "msg" && poller != nil {
				// メッセージが来たチャンネルは viewer poller の優先度を上げます。
				poller.noteMessage(trigger.Ch)
			}
			// フロントはこの trigger で光の粒・ビーム・履歴表示を更新します。
			writeSSE(w, "trigger", trigger)
			flusher.Flush()
		case snapshot, ok := <-viewers:
			if !ok {
				// viewer ストリームが終わったら、この case を無効化して他のイベント処理を続けます。
				viewers = nil
				continue
			}
			// live モードの閲覧者一覧を HUD 表示用に送ります。
			writeSSE(w, "viewers", snapshot)
			flusher.Flush()
		case err, ok := <-errc:
			if ok && err != nil {
				// WebSocket など下流で致命的なエラーが出た場合は、画面に理由を出して終了します。
				writeSSE(w, "stream-error", map[string]string{"error": err.Error()})
				flusher.Flush()
			}
			return
		}
	}
}

func streamStatus(demo bool) string {
	// フロントのステータス表示で、demo/live のどちらにつながったかを明示します。
	if demo {
		return "demo connected"
	}
	return "traQ connected"
}

func isTriggerForActiveChannel(trigger triggerPayload, channelIDs map[string]bool) bool {
	// チャンネル一覧が取れていない場合、live イベントは安全側で捨てます。
	if len(channelIDs) == 0 {
		return false
	}
	// msg は発生チャンネル、mov は移動先チャンネルが表示対象かどうかで判定します。
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
	// out は handleEvents に渡す可視化イベント、errc は interface を live と揃えるために返します。
	out := make(chan triggerPayload)
	errc := make(chan error)

	go func() {
		// goroutine 終了時に両チャンネルを閉じ、handleEvents 側が終了を検知できるようにします。
		defer close(out)
		defer close(errc)

		// demo は短い間隔でイベントを出し、認証なしでも動きが見える状態にします。
		ticker := time.NewTicker(900 * time.Millisecond)
		defer ticker.Stop()
		// ユーザーごとの現在地を覚え、mov イベントの from を自然にします。
		userChannels := map[string]string{}
		users := []string{"demo-user-a", "demo-user-b", "demo-user-c", "demo-user-d"}
		count := 0

		for {
			select {
			case <-ctx.Done():
				// ブラウザが切断したら demo 生成も止めます。
				return
			case <-ticker.C:
				// ランダムな末端チャンネルを選び、光らせる対象にします。
				to := s.state.randomChannelID()
				if count%3 == 0 {
					// 3 回に 1 回はメッセージ発生として、チャンネルと祖先を強く光らせます。
					out <- triggerPayload{Type: "msg", Ch: to}
				} else {
					// それ以外はユーザー移動として、from から to へビームを出します。
					usr := users[rand.Intn(len(users))]
					out <- triggerPayload{Type: "mov", Usr: usr, From: userChannels[usr], To: to}
					userChannels[usr] = to
				}
				// count は msg/mov の比率を作るためだけに使います。
				count++
			}
		}
	}()

	return out, errc
}
