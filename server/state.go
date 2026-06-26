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
	// デモ用チャンネルの LastSyncTime を同じ時刻で初期化します。
	now := time.Now()
	// grandRootID はフロントの 3D 配置で全島の親になる仮想ルートです。
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

	// demo モードで見栄えするよう、traQ らしい複数の大分類を用意します。
	rootNames := []string{"general", "random", "event", "team", "times", "project", "creative", "tech"}
	for i, name := range rootNames {
		// island は 3D 空間で離れて配置される一つのまとまりです。
		rootID := fmt.Sprintf("island-%d", i+1)
		// grand root の children に island を登録し、木構造として辿れるようにします。
		channels[grandRootID].Children = append(channels[grandRootID].Children, rootID)
		// island 自体のノードを作り、depth=1 としてルート直下に置きます。
		channels[rootID] = &channel{
			ID:           rootID,
			Name:         name,
			ParentID:     grandRootID,
			IslandID:     i,
			Depth:        1,
			LastSyncTime: now,
		}

		// 各 island の下に中間チャンネルを 5 個作り、可視化に枝分かれを持たせます。
		for j := 0; j < 5; j++ {
			childID := fmt.Sprintf("%s-ch-%d", rootID, j+1)
			// 親の children に子 ID を追加して、init payload のツリーに反映します。
			channels[rootID].Children = append(channels[rootID].Children, childID)
			// 中間チャンネルは depth=2 として、leaf より太めに描かれます。
			channels[childID] = &channel{
				ID:           childID,
				Name:         fmt.Sprintf("%s/%02d", name, j+1),
				ParentID:     rootID,
				IslandID:     i,
				Depth:        2,
				LastSyncTime: now,
			}

			// さらに leaf を 2 個ずつ作り、demo のランダムイベント対象にします。
			for k := 0; k < 2; k++ {
				leafID := fmt.Sprintf("%s-sub-%d", childID, k+1)
				// leaf も親の children に登録して、フロントが親子リンクを描けるようにします。
				channels[childID].Children = append(channels[childID].Children, leafID)
				// leaf は depth=3 の末端ノードで、randomChannelID の候補になります。
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

	// users は mov イベントの from 推定に使う、ユーザーごとの現在地メモリです。
	return &stateManager{
		channels: channels,
		users:    map[string]*userState{},
	}
}

func (sm *stateManager) initJSON() ([]byte, error) {
	// チャンネル構造を読み取るだけなので read lock を使います。
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// フロントの init イベントでそのまま送れる JSON payload を組み立てます。
	payload := initPayload{Channels: make(map[string]initChannel, len(sm.channels))}
	for id, ch := range sm.channels {
		// Children は append でコピーし、内部 slice を外へ共有しないようにします。
		payload.Channels[id] = initChannel{
			ID:       ch.ID,
			Name:     ch.Name,
			ParentID: ch.ParentID,
			Children: append([]string(nil), ch.Children...),
			IslandID: ch.IslandID,
			Depth:    ch.Depth,
		}
	}
	// SSE の init では []byte をそのまま流すため、ここで JSON 化して返します。
	return json.Marshal(payload)
}

func (sm *stateManager) applyTrigger(trigger triggerPayload) (triggerPayload, bool) {
	// msg/mov はチャンネル score とユーザー現在地を更新するため write lock を取ります。
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// trigger.Type ごとに、サーバ内状態への反映方法を切り替えます。
	switch trigger.Type {
	case "msg":
		// メッセージは強い発火として、対象チャンネルと祖先に大きめの熱量を足します。
		sm.addScoreLocked(trigger.Ch, 46)
	case "mov":
		// mov はユーザー ID と移動先があるときだけ、現在地を更新できます。
		if trigger.Usr != "" && trigger.To != "" {
			// 初めて見るユーザーなら、その場で状態エントリを作ります。
			user, ok := sm.users[trigger.Usr]
			if !ok {
				user = &userState{UserID: trigger.Usr}
				sm.users[trigger.Usr] = user
			}
			// WebSocket 由来の mov は from を持たないため、前回の現在地で補完します。
			if trigger.From == "" {
				trigger.From = user.CurrentChannel
			}
			// 同じチャンネルへの移動は見た目に変化がないので、フロントへ送らないよう false を返します。
			if trigger.From == trigger.To {
				return trigger, false
			}
			// 次回の from 補完のため、ユーザーの現在地を移動先へ更新します。
			user.CurrentChannel = trigger.To
			user.LastUpdated = time.Now()
		}
		// ユーザー移動は軽い発火として、移動先チャンネルと祖先に熱量を足します。
		sm.addScoreLocked(trigger.To, 11)
	}
	// 表示してよい trigger は true と一緒に返します。
	return trigger, true
}

func (sm *stateManager) addScoreLocked(channelID string, amount float64) {
	// 呼び出し元が lock 済みである前提なので、ここでは lock を取り直しません。
	for depth := 0; channelID != ""; depth++ {
		// チャンネルが見つからなければ、それ以上祖先も辿れないため終了します。
		ch, ok := sm.channels[channelID]
		if !ok {
			return
		}
		// 親へ遡るほど 0.45 倍ずつ弱め、発火地点が一番強く光るようにします。
		ch.Score = math.Min(100, ch.Score+amount*math.Pow(0.45, float64(depth)))
		// 次のループで親チャンネルへ進みます。
		channelID = ch.ParentID
	}
}

func (sm *stateManager) syncPayload() syncPayload {
	// 減衰と LastSync 更新を伴うため write lock を取ります。
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 現在時刻を基準に、前回同期からの経過秒数で指数減衰を計算します。
	now := time.Now()
	deltas := make(map[string]float64)
	for _, ch := range sm.channels {
		// チャンネルごとに最後に同期した時刻からの経過を取ります。
		elapsed := now.Sub(ch.LastSyncTime).Seconds()
		// 24 秒を時定数にしてスコアをなだらかに減衰させます。
		ch.Score *= math.Exp(-elapsed / 24)
		// 差が小さいときは送らず、一定時間たった低い値だけ補正用に送ります。
		if math.Abs(ch.Score-ch.LastSyncScore) >= 0.35 || (ch.Score > 0.1 && elapsed >= 8) {
			// フロントへ送る値は小数 1 桁に丸め、payload を安定させます。
			deltas[ch.ID] = math.Round(ch.Score*10) / 10
			// 次回差分判定の基準値と時刻を更新します。
			ch.LastSyncScore = ch.Score
			ch.LastSyncTime = now
		}
	}
	// フロントは TS と Deltas を受け取り、各ノードの targetScore を更新します。
	return syncPayload{TS: now.Unix(), Deltas: deltas}
}

func (sm *stateManager) randomChannelID() string {
	// demo イベントの対象選択なので、チャンネル一覧を read lock で読みます。
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// 末端チャンネルだけを候補にして、demo の発火点が枝の先に出やすくします。
	candidates := make([]string, 0, len(sm.channels))
	for id, ch := range sm.channels {
		if id != grandRootID && len(ch.Children) == 0 {
			candidates = append(candidates, id)
		}
	}
	// 念のため候補がなければ grand root を返して panic を避けます。
	if len(candidates) == 0 {
		return grandRootID
	}
	// 候補からランダムに 1 つ選び、demo の msg/mov 対象にします。
	return candidates[rand.Intn(len(candidates))]
}
