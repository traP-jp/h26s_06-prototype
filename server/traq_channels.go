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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.traqBaseURL+"/api/v3/channels", nil)
	if err != nil {
		return channelData{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return channelData{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return channelData{}, fmt.Errorf("channels endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var channels traqChannelList
	if err := json.Unmarshal(body, &channels); err != nil {
		return channelData{}, err
	}
	activeChannels := filterActiveTraqChannels(channels.Public)
	initJSON, err := buildTraqInitPayload(activeChannels)
	if err != nil {
		return channelData{}, err
	}
	return channelData{
		Channels:   activeChannels,
		ChannelIDs: buildTraqChannelIDSet(activeChannels),
		InitJSON:   initJSON,
	}, nil
}

func filterActiveTraqChannels(channels []traqChannel) []traqChannel {
	byID := make(map[string]traqChannel, len(channels))
	for _, ch := range channels {
		if ch.ID != "" {
			byID[ch.ID] = ch
		}
	}

	includeCache := make(map[string]bool, len(channels))
	activeChannels := make([]traqChannel, 0, len(channels))
	for _, ch := range channels {
		if isTraqChannelActiveWithAncestors(ch.ID, byID, includeCache, map[string]bool{}) {
			activeChannels = append(activeChannels, ch)
		}
	}
	return activeChannels
}

func isTraqChannelActiveWithAncestors(channelID string, channels map[string]traqChannel, cache map[string]bool, visiting map[string]bool) bool {
	if channelID == "" {
		return false
	}
	if active, ok := cache[channelID]; ok {
		return active
	}
	if visiting[channelID] {
		cache[channelID] = false
		return false
	}

	ch, ok := channels[channelID]
	if !ok || ch.ID == "" || ch.Archived {
		cache[channelID] = false
		return false
	}
	if ch.ParentID == nil || *ch.ParentID == "" {
		cache[channelID] = true
		return true
	}

	visiting[channelID] = true
	active := isTraqChannelActiveWithAncestors(*ch.ParentID, channels, cache, visiting)
	delete(visiting, channelID)
	cache[channelID] = active
	return active
}

func buildTraqChannelIDSet(channels []traqChannel) map[string]bool {
	channelIDs := make(map[string]bool, len(channels))
	for _, ch := range channels {
		if ch.ID != "" && !ch.Archived {
			channelIDs[ch.ID] = true
		}
	}
	return channelIDs
}

func buildTraqInitPayload(channels []traqChannel) ([]byte, error) {
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

	included := make(map[string]traqChannel, len(channels))
	for _, ch := range channels {
		if ch.ID == "" || ch.Archived {
			continue
		}
		included[ch.ID] = ch
	}

	for _, ch := range channels {
		if _, ok := included[ch.ID]; !ok {
			continue
		}
		parentID := grandRootID
		if ch.ParentID != nil {
			if _, ok := included[*ch.ParentID]; ok {
				parentID = *ch.ParentID
			}
		}
		payload.Channels[ch.ID] = initChannel{
			ID:       ch.ID,
			Name:     ch.Name,
			ParentID: parentID,
			Children: []string{},
		}
	}

	for _, ch := range channels {
		if _, ok := payload.Channels[ch.ID]; !ok {
			continue
		}
		parentID := payload.Channels[ch.ID].ParentID
		parent := payload.Channels[parentID]
		parent.Children = append(parent.Children, ch.ID)
		payload.Channels[parentID] = parent
	}

	for islandID, rootID := range payload.Channels[grandRootID].Children {
		assignInitChannelLayout(payload.Channels, rootID, islandID, 1)
	}

	return json.Marshal(payload)
}

func assignInitChannelLayout(channels map[string]initChannel, id string, islandID int, depth int) {
	ch, ok := channels[id]
	if !ok {
		return
	}
	ch.IslandID = islandID
	ch.Depth = depth
	channels[id] = ch
	for _, childID := range ch.Children {
		assignInitChannelLayout(channels, childID, islandID, depth+1)
	}
}
