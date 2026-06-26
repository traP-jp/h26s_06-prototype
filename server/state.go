package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// このファイルは、チャンネルの熱量とユーザー移動状態をメモリ上で管理します。
func newStateManager() *stateManager {
	now := time.Now()
	channels := map[string]*channel{
		grandRootID: {
			ID:           grandRootID,
			Name:         "Grand Root",
			ParentID:     "",
			IslandID:     -1,
			Depth:        0,
			LastSyncTime: now,
		},
	}

	rootNames := []string{"general", "random", "event", "team", "times", "project", "creative", "tech"}
	for i, name := range rootNames {
		rootID := fmt.Sprintf("island-%d", i+1)
		channels[grandRootID].Children = append(channels[grandRootID].Children, rootID)
		channels[rootID] = &channel{
			ID:           rootID,
			Name:         name,
			ParentID:     grandRootID,
			IslandID:     i,
			Depth:        1,
			LastSyncTime: now,
		}

		for j := 0; j < 5; j++ {
			childID := fmt.Sprintf("%s-ch-%d", rootID, j+1)
			channels[rootID].Children = append(channels[rootID].Children, childID)
			channels[childID] = &channel{
				ID:           childID,
				Name:         fmt.Sprintf("%s/%02d", name, j+1),
				ParentID:     rootID,
				IslandID:     i,
				Depth:        2,
				LastSyncTime: now,
			}

			for k := 0; k < 2; k++ {
				leafID := fmt.Sprintf("%s-sub-%d", childID, k+1)
				channels[childID].Children = append(channels[childID].Children, leafID)
				channels[leafID] = &channel{
					ID:           leafID,
					Name:         fmt.Sprintf("%s/%02d/%02d", name, j+1, k+1),
					ParentID:     childID,
					IslandID:     i,
					Depth:        3,
					LastSyncTime: now,
				}
			}
		}
	}

	return &stateManager{
		channels: channels,
		users:    map[string]*userState{},
	}
}

func (sm *stateManager) initJSON() ([]byte, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	payload := initPayload{Channels: make(map[string]initChannel, len(sm.channels))}
	for id, ch := range sm.channels {
		payload.Channels[id] = initChannel{
			ID:       ch.ID,
			Name:     ch.Name,
			ParentID: ch.ParentID,
			Children: append([]string(nil), ch.Children...),
			IslandID: ch.IslandID,
			Depth:    ch.Depth,
		}
	}
	return json.Marshal(payload)
}

func (sm *stateManager) applyTrigger(trigger triggerPayload) (triggerPayload, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch trigger.Type {
	case "msg":
		sm.addScoreLocked(trigger.Ch, 46)
	case "mov":
		if trigger.Usr != "" && trigger.To != "" {
			user, ok := sm.users[trigger.Usr]
			if !ok {
				user = &userState{UserID: trigger.Usr}
				sm.users[trigger.Usr] = user
			}
			if trigger.From == "" {
				trigger.From = user.CurrentChannel
			}
			if trigger.From == trigger.To {
				return trigger, false
			}
			user.CurrentChannel = trigger.To
			user.LastUpdated = time.Now()
		}
		sm.addScoreLocked(trigger.To, 11)
	}
	return trigger, true
}

func (sm *stateManager) addScoreLocked(channelID string, amount float64) {
	for depth := 0; channelID != ""; depth++ {
		ch, ok := sm.channels[channelID]
		if !ok {
			return
		}
		ch.Score = math.Min(100, ch.Score+amount*math.Pow(0.45, float64(depth)))
		channelID = ch.ParentID
	}
}

func (sm *stateManager) syncPayload() syncPayload {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	deltas := make(map[string]float64)
	for _, ch := range sm.channels {
		elapsed := now.Sub(ch.LastSyncTime).Seconds()
		ch.Score *= math.Exp(-elapsed / 24)
		if math.Abs(ch.Score-ch.LastSyncScore) >= 0.35 || (ch.Score > 0.1 && elapsed >= 8) {
			deltas[ch.ID] = math.Round(ch.Score*10) / 10
			ch.LastSyncScore = ch.Score
			ch.LastSyncTime = now
		}
	}
	return syncPayload{TS: now.Unix(), Deltas: deltas}
}

func (sm *stateManager) randomChannelID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	candidates := make([]string, 0, len(sm.channels))
	for id, ch := range sm.channels {
		if id != grandRootID && len(ch.Children) == 0 {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return grandRootID
	}
	return candidates[rand.Intn(len(candidates))]
}
