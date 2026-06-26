package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// このファイルは traQ のチャンネル一覧取得と、可視化用ツリーへの変換を担当します。
func (s *server) fetchChannelData(ctx context.Context, accessToken string) (channelData, error) {
	// traQ の全チャンネル一覧 API を live 接続の context 付きで作ります。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.traqBaseURL+"/api/v3/channels", nil)
	if err != nil {
		return channelData{}, err
	}
	// OAuth で得た token を Bearer として付け、ユーザーが見られるチャンネル一覧を取得します。
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// 共通 HTTP client で traQ API を呼びます。
	resp, err := s.client.Do(req)
	if err != nil {
		return channelData{}, err
	}
	defer resp.Body.Close()
	// チャンネル一覧は大きめなので 8MiB まで読み込み、異常な巨大レスポンスを避けます。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 非 2xx のときは本文も含めて、SSE の stream-error に出しやすくします。
		return channelData{}, fmt.Errorf("channels endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// traQ の public チャンネル配列を取り出します。
	var channels traqChannelList
	if err := json.Unmarshal(body, &channels); err != nil {
		return channelData{}, err
	}
	// archived なチャンネルと、その子孫を可視化対象から除外します。
	activeChannels := filterActiveTraqChannels(channels.Public)
	// フロントの init イベントで使う、grand_root 付きの木構造 JSON を作ります。
	initJSON, err := buildTraqInitPayload(activeChannels)
	if err != nil {
		return channelData{}, err
	}
	// live stream 中の trigger フィルタ用 ID set も一緒に返します。
	return channelData{
		Channels:   activeChannels,
		ChannelIDs: buildTraqChannelIDSet(activeChannels),
		InitJSON:   initJSON,
	}, nil
}

func filterActiveTraqChannels(channels []traqChannel) []traqChannel {
	// 親をたどって archived ancestor を調べるため、ID からチャンネルを引ける map を作ります。
	byID := make(map[string]traqChannel, len(channels))
	for _, ch := range channels {
		if ch.ID != "" {
			byID[ch.ID] = ch
		}
	}

	// includeCache は同じ親子チェーンを何度も調べないためのメモです。
	includeCache := make(map[string]bool, len(channels))
	activeChannels := make([]traqChannel, 0, len(channels))
	for _, ch := range channels {
		// 自分自身と祖先が active なチャンネルだけを残します。
		if isTraqChannelActiveWithAncestors(ch.ID, byID, includeCache, map[string]bool{}) {
			activeChannels = append(activeChannels, ch)
		}
	}
	// 返す順序は traQ API の順序を保ちます。
	return activeChannels
}

func isTraqChannelActiveWithAncestors(channelID string, channels map[string]traqChannel, cache map[string]bool, visiting map[string]bool) bool {
	// ID が空の行は可視化対象にできません。
	if channelID == "" {
		return false
	}
	// 既に判定済みならキャッシュ結果を返します。
	if active, ok := cache[channelID]; ok {
		return active
	}
	// 親子関係が壊れて循環している場合は、安全側で除外します。
	if visiting[channelID] {
		cache[channelID] = false
		return false
	}

	// チャンネルが存在しない、ID が空、または archived なら除外します。
	ch, ok := channels[channelID]
	if !ok || ch.ID == "" || ch.Archived {
		cache[channelID] = false
		return false
	}
	// 親がなければ root 相当なので、自分が archived でなければ active とみなします。
	if ch.ParentID == nil || *ch.ParentID == "" {
		cache[channelID] = true
		return true
	}

	// 再帰中マークを付けてから親を判定し、循環検知に使います。
	visiting[channelID] = true
	// 親が active でなければ、子も可視化対象から外します。
	active := isTraqChannelActiveWithAncestors(*ch.ParentID, channels, cache, visiting)
	// このチャンネルの再帰が終わったので visiting から外します。
	delete(visiting, channelID)
	// 次回同じチャンネルを調べるときのために結果を保存します。
	cache[channelID] = active
	return active
}

func buildTraqChannelIDSet(channels []traqChannel) map[string]bool {
	// live trigger の高速フィルタ用に、active channel ID の set を作ります。
	channelIDs := make(map[string]bool, len(channels))
	for _, ch := range channels {
		if ch.ID != "" && !ch.Archived {
			channelIDs[ch.ID] = true
		}
	}
	// map lookup で O(1) 判定できる形にして返します。
	return channelIDs
}

func buildTraqInitPayload(channels []traqChannel) ([]byte, error) {
	// フロントの ChannelGraph は単一 root を期待するため、仮想の grand_root を必ず入れます。
	payload := initPayload{Channels: map[string]initChannel{
		grandRootID: {
			ID:       grandRootID,
			Name:     "Grand Root",
			ParentID: "",
			Children: []string{},
			IslandID: -1,
			Depth:    0,
		},
	}}

	// まず含めるチャンネルだけを map 化し、親が含まれるかを後で判定できるようにします。
	included := make(map[string]traqChannel, len(channels))
	for _, ch := range channels {
		if ch.ID == "" || ch.Archived {
			// 念のためここでも空 ID と archived を除外します。
			continue
		}
		included[ch.ID] = ch
	}

	// 各 traQ チャンネルを、フロント用の initChannel に変換します。
	for _, ch := range channels {
		if _, ok := included[ch.ID]; !ok {
			continue
		}
		// 親が含まれていない場合は grand_root 直下にぶら下げます。
		parentID := grandRootID
		if ch.ParentID != nil {
			if _, ok := included[*ch.ParentID]; ok {
				// 親も active なら traQ の親子関係をそのまま使います。
				parentID = *ch.ParentID
			}
		}
		// children は次のループで親側へ追加するため、ここでは空で初期化します。
		payload.Channels[ch.ID] = initChannel{
			ID:       ch.ID,
			Name:     ch.Name,
			ParentID: parentID,
			Children: []string{},
		}
	}

	// 親 ID を見ながら children 配列を復元します。
	for _, ch := range channels {
		if _, ok := payload.Channels[ch.ID]; !ok {
			continue
		}
		// 子側に入れた ParentID を取り出し、親の Children に自分を追加します。
		parentID := payload.Channels[ch.ID].ParentID
		parent := payload.Channels[parentID]
		parent.Children = append(parent.Children, ch.ID)
		// map の値はコピーなので、更新後の parent を map に戻します。
		payload.Channels[parentID] = parent
	}

	// grand_root 直下の子を island とみなし、3D 配置用の island/depth を再帰的に付けます。
	for islandID, rootID := range payload.Channels[grandRootID].Children {
		assignInitChannelLayout(payload.Channels, rootID, islandID, 1)
	}

	// SSE init でそのまま送るため、ここで JSON に変換します。
	return json.Marshal(payload)
}

func assignInitChannelLayout(channels map[string]initChannel, id string, islandID int, depth int) {
	// ID が存在しない場合は、壊れた children 参照として何もせず戻ります。
	ch, ok := channels[id]
	if !ok {
		return
	}
	// islandID は root ごとの色/配置まとまり、depth はノードサイズや距離に使います。
	ch.IslandID = islandID
	ch.Depth = depth
	// map の値はコピーなので、更新した ch を map に戻します。
	channels[id] = ch
	// 子へ同じ islandID と depth+1 を伝播させます。
	for _, childID := range ch.Children {
		assignInitChannelLayout(channels, childID, islandID, depth+1)
	}
}
