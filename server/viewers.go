package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// このファイルは live モードでチャンネル閲覧者を間引きながらポーリングします。
func newViewerPoller(channels []traqChannel, maxPerTick int) *viewerPoller {
	// API 呼び出し対象にできる、ID がありアーカイブされていないチャンネルだけを残します。
	activeChannels := make([]traqChannel, 0, len(channels))
	for _, ch := range channels {
		if ch.ID != "" && !ch.Archived {
			activeChannels = append(activeChannels, ch)
		}
	}
	// 設定値が 0 以下、または対象数を超える場合は、全チャンネルを 1 tick で見る設定に補正します。
	if maxPerTick <= 0 || maxPerTick > len(activeChannels) {
		maxPerTick = len(activeChannels)
	}
	// messageWeight は MESSAGE_CREATED があったチャンネルを次回以降優先するための重みです。
	return &viewerPoller{
		channels:      activeChannels,
		messageWeight: map[string]float64{},
		maxPerTick:    maxPerTick,
	}
}

func (p *viewerPoller) noteMessage(channelID string) {
	// poller がない demo mode や、チャンネル ID が空のイベントは何もしません。
	if p == nil || channelID == "" {
		return
	}
	// WebSocket 側と poller 側が同時に触るため mutex で保護します。
	p.mu.Lock()
	defer p.mu.Unlock()
	// 最近メッセージがあったチャンネルは閲覧者変化も起きやすいので、サンプリング重みを上げます。
	p.messageWeight[channelID] = math.Min(120, p.messageWeight[channelID]+12)
}

func (p *viewerPoller) sampleChannels() []traqChannel {
	// poller が未設定なら、呼び出し側が何も取得しないよう nil を返します。
	if p == nil {
		return nil
	}
	// channels と messageWeight を同時に読む/更新するため mutex で保護します。
	p.mu.Lock()
	defer p.mu.Unlock()

	// 対象数が上限以下なら、間引かず全チャンネルを返します。
	if len(p.channels) <= p.maxPerTick {
		return append([]traqChannel(nil), p.channels...)
	}

	// weightedChannel に変換し、最近メッセージがあったチャンネルほど選ばれやすくします。
	candidates := make([]weightedChannel, 0, len(p.channels))
	for _, ch := range p.channels {
		// messageWeight がなければ 0 なので、最低重みは 1 になります。
		messageWeight := p.messageWeight[ch.ID]
		candidates = append(candidates, weightedChannel{
			channel: ch,
			weight:  1 + messageWeight,
		})
		// 重みが十分小さくなったものは map から消して、状態を肥大化させないようにします。
		if messageWeight < 0.05 {
			delete(p.messageWeight, ch.ID)
		} else {
			// tick ごとに重みを減衰させ、古いメッセージの影響が自然に薄れるようにします。
			p.messageWeight[ch.ID] = messageWeight * 0.82
		}
	}

	// 重み付き抽選で maxPerTick 件まで重複なしに選びます。
	selected := make([]traqChannel, 0, p.maxPerTick)
	for len(selected) < p.maxPerTick && len(candidates) > 0 {
		// 現在の候補全体の重み合計を計算します。
		totalWeight := 0.0
		for _, candidate := range candidates {
			totalWeight += candidate.weight
		}
		// 0..totalWeight の乱数を作り、累積重みで選択位置を決めます。
		pick := rand.Float64() * totalWeight
		selectedIndex := 0
		for i, candidate := range candidates {
			pick -= candidate.weight
			if pick <= 0 {
				// pick が 0 以下になった候補を今回の当選チャンネルにします。
				selectedIndex = i
				break
			}
		}
		// 当選チャンネルを結果へ追加します。
		selected = append(selected, candidates[selectedIndex].channel)
		// 同じ tick で重複して選ばれないよう、候補から削除します。
		candidates = append(candidates[:selectedIndex], candidates[selectedIndex+1:]...)
	}

	// 呼び出し側はここで返ったチャンネルだけ viewers API を叩きます。
	return selected
}

func (s *server) streamViewerSnapshots(ctx context.Context, accessToken string, poller *viewerPoller) <-chan viewerSnapshotPayload {
	// out は handleEvents へ viewer snapshot を渡すためのチャンネルです。
	out := make(chan viewerSnapshotPayload)

	go func() {
		// SSE が閉じたとき、呼び出し側に終了を伝えるため close します。
		defer close(out)

		// 設定された間隔で viewer API を poll します。
		ticker := time.NewTicker(s.cfg.viewerPollInterval)
		defer ticker.Stop()

		// previous は前回までに観測した viewer 行を保持し、サンプル外チャンネルの状態も残します。
		previous := map[string]viewerRow{}
		for {
			// 1 tick 分の viewer snapshot と、今回サンプリングしたチャンネル集合を取得します。
			snapshot, current, sampledChannelIDs, err := s.fetchViewerSnapshot(ctx, accessToken, poller)
			if err != nil {
				if ctx.Err() == nil {
					// 一部 API エラーは stream 全体を止めず、次 tick で再試行します。
					log.Printf("viewer snapshot skipped: %v", err)
				}
			} else {
				// 今回見たチャンネルについて、前回との差分をログに出します。
				logViewerChanges(filterViewerRows(previous, sampledChannelIDs), current)
				// サンプルしたチャンネルだけを current で置き換え、未サンプルチャンネルの前回値は残します。
				mergeViewerRows(previous, current, sampledChannelIDs)
				// snapshot.Total は「保持している最新状態全体」の数に更新します。
				snapshot.Total = len(previous)
				select {
				// handleEvents へ送信し、SSE の viewers イベントになります。
				case out <- snapshot:
				// 切断時は送信待ちをやめて終了します。
				case <-ctx.Done():
					return
				}
			}

			select {
			case <-ctx.Done():
				// SSE 切断時に poller goroutine を止めます。
				return
			case <-ticker.C:
				// 次の poll 間隔まで待ってからループします。
			}
		}
	}()

	return out
}

func (s *server) fetchViewerSnapshot(ctx context.Context, accessToken string, poller *viewerPoller) (viewerSnapshotPayload, map[string]viewerRow, map[string]bool, error) {
	// 並列取得結果を、対象チャンネル・viewer 一覧・エラーのセットで受け取ります。
	type result struct {
		channel traqChannel
		viewers []traqChannelViewer
		err     error
	}

	// 今回 API を叩くチャンネルだけを poller から選びます。
	activeChannels := poller.sampleChannels()
	// 後で previous を merge するとき、今回サンプルしたチャンネルを識別するための set です。
	sampledChannelIDs := make(map[string]bool, len(activeChannels))
	for _, ch := range activeChannels {
		sampledChannelIDs[ch.ID] = true
	}

	// 同時リクエスト数を 8 に制限し、traQ API へ過度な負荷をかけないようにします。
	sem := make(chan struct{}, 8)
	// results は全 goroutine 分の結果を受ける buffered channel です。
	results := make(chan result, len(activeChannels))
	var wg sync.WaitGroup
	for _, ch := range activeChannels {
		// ループ変数を goroutine ごとに固定します。
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				// 処理終了時にセマフォを返し、次の goroutine が進めるようにします。
				defer func() { <-sem }()
			case <-ctx.Done():
				// キャンセル済みなら API を呼ばず、ctx エラーとして返します。
				results <- result{channel: ch, err: ctx.Err()}
				return
			}
			// 対象チャンネルの viewers API を呼びます。
			viewers, err := s.fetchChannelViewers(ctx, accessToken, ch.ID)
			// 成功/失敗どちらも results に流し、集計側で扱います。
			results <- result{channel: ch, viewers: viewers, err: err}
		}()
	}

	// 全チャンネルの取得が終わってから results を閉じます。
	wg.Wait()
	close(results)

	// rows は recent 表示用、summaries はチャンネル別集計用です。
	rows := make([]viewerRow, 0)
	summaries := make([]viewerChannelSummary, 0)
	// current は viewerKey ごとの最新行として merge に使います。
	current := map[string]viewerRow{}
	var firstErr error
	for res := range results {
		if res.err != nil {
			// 複数エラーがあっても、返すのは最初の 1 つにします。
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		// viewer がいないチャンネルは summaries に出さず、HUD を短くします。
		if len(res.viewers) == 0 {
			continue
		}

		// チャンネル単位の閲覧者数と状態別カウントを作ります。
		summary := viewerChannelSummary{
			ChannelID:   res.channel.ID,
			ChannelName: res.channel.Name,
			Count:       len(res.viewers),
		}
		for _, viewer := range res.viewers {
			// traQ viewer state を HUD の状態別カウントへ分類します。
			switch viewer.State {
			case "editing":
				summary.Editing++
			case "monitoring":
				summary.Monitoring++
			case "stale_viewing":
				summary.Stale++
			}
			// recent 表示用に、viewer とチャンネル情報を 1 行へまとめます。
			row := viewerRow{
				UserID:      viewer.UserID,
				ChannelID:   res.channel.ID,
				ChannelName: res.channel.Name,
				State:       viewer.State,
				UpdatedAt:   viewer.UpdatedAt,
			}
			rows = append(rows, row)
			// user/channel の組をキーにして、同じ viewer 行を最新値で上書きします。
			current[viewerKey(row)] = row
		}
		// このチャンネルの summary を一覧へ追加します。
		summaries = append(summaries, summary)
	}
	// すべて失敗して viewer 行もない場合は、snapshot として使えないのでエラーにします。
	if firstErr != nil && len(rows) == 0 {
		return viewerSnapshotPayload{}, nil, nil, firstErr
	}

	// チャンネル集計は閲覧者数の多い順、同数なら名前順で安定させます。
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Count == summaries[j].Count {
			return summaries[i].ChannelName < summaries[j].ChannelName
		}
		return summaries[i].Count > summaries[j].Count
	})
	// recent 行は UpdatedAt の新しい順に並べます。
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	// フロントに送りすぎないよう、チャンネル集計は最大 12 件に切ります。
	if len(summaries) > 12 {
		summaries = summaries[:12]
	}
	// recent 行も最大 24 件に切り、payload と HUD を小さく保ちます。
	if len(rows) > 24 {
		rows = rows[:24]
	}

	// SSE の viewers event として送る payload と、merge 用の current/set を返します。
	return viewerSnapshotPayload{
		TS:              time.Now().Unix(),
		Total:           len(current),
		SampledChannels: len(activeChannels),
		TotalChannels:   len(poller.channels),
		Channels:        summaries,
		Recent:          rows,
	}, current, sampledChannelIDs, nil
}

func (s *server) fetchChannelViewers(ctx context.Context, accessToken string, channelID string) ([]traqChannelViewer, error) {
	// channelID は URL パスに入るため PathEscape してから viewers endpoint を作ります。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.traqBaseURL+"/api/v3/channels/"+url.PathEscape(channelID)+"/viewers", nil)
	if err != nil {
		return nil, err
	}
	// live 接続で得た traQ token を使い、対象チャンネルの閲覧者を取得します。
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// 共通 HTTP client で API を呼びます。
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// エラー本文を出せるよう、成功時も含めて上限付きで読み込みます。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// チャンネルが消えている/見えない場合は、viewer なしとして扱い stream を止めません。
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	// 404 以外の非 2xx は一時的な API エラーとして呼び出し元へ返します。
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("channel viewers endpoint returned %s for %s: %s", resp.Status, channelID, strings.TrimSpace(string(body)))
	}

	// traQ のレスポンスを viewer 配列へ変換します。
	var viewers []traqChannelViewer
	if err := json.Unmarshal(body, &viewers); err != nil {
		return nil, err
	}
	// 呼び出し元でチャンネル名などを付与して viewerRow に変換します。
	return viewers, nil
}

func filterViewerRows(rows map[string]viewerRow, channelIDs map[string]bool) map[string]viewerRow {
	// ログ差分を出すとき、今回サンプリングしたチャンネルの前回値だけに絞ります。
	filtered := make(map[string]viewerRow)
	for key, row := range rows {
		if channelIDs[row.ChannelID] {
			// sampledChannelIDs に含まれる行だけを比較対象として残します。
			filtered[key] = row
		}
	}
	// 未サンプリングチャンネルは「離脱」と誤判定しないよう、ここでは含めません。
	return filtered
}

func mergeViewerRows(rows map[string]viewerRow, current map[string]viewerRow, sampledChannelIDs map[string]bool) {
	// 今回サンプリングしたチャンネルの古い行は、current で置き換えるため先に消します。
	for key, row := range rows {
		if sampledChannelIDs[row.ChannelID] {
			delete(rows, key)
		}
	}
	// 今回取得した最新行を previous 相当の rows に反映します。
	for key, row := range current {
		rows[key] = row
	}
}

func logViewerChanges(previous, current map[string]viewerRow) {
	// 初回など比較元がない場合はログを出さずに終わります。
	if previous == nil {
		return
	}
	// current にいて previous にいない viewer は入室としてログします。
	for key, row := range current {
		prev, ok := previous[key]
		if !ok {
			log.Printf("viewer entered: user=%s channel=%s state=%s", row.UserID, row.ChannelName, row.State)
			continue
		}
		// 同じ user/channel でも state が変わった場合は状態変化としてログします。
		if prev.State != row.State {
			log.Printf("viewer state changed: user=%s channel=%s %s->%s", row.UserID, row.ChannelName, prev.State, row.State)
		}
	}
	// previous にいて current にいない viewer は離脱としてログします。
	for key, row := range previous {
		if _, ok := current[key]; !ok {
			log.Printf("viewer left: user=%s channel=%s state=%s", row.UserID, row.ChannelName, row.State)
		}
	}
}

func viewerKey(row viewerRow) string {
	// 同じユーザーが複数チャンネルに出る可能性を考え、user/channel の組をキーにします。
	return row.UserID + "|" + row.ChannelID
}
