package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// このファイルは、サーバ全体で共有する設定・状態・traQ API 用 DTO をまとめます。
// grandRootID は traQ 由来ではない仮想ルートで、フロントの木構造を 1 つにまとめます。
const grandRootID = "grand_root"

// config は環境変数から読み込んだアプリ全体の設定です。
type config struct {
	// addr は Go HTTP server の listen アドレスです。
	addr string
	// traqBaseURL は traQ API のベース URL です。
	traqBaseURL string
	// clientID は OAuth 認可リクエストに使う traQ client ID です。
	clientID string
	// redirectURL は traQ OAuth から戻る callback URL です。
	redirectURL string
	// scope は OAuth で要求する権限です。
	scope string
	// appOrigin は CORS と OAuth 後の戻り先に使うフロント URL です。
	appOrigin string
	// viewerPollInterval は viewers API を poll する間隔です。
	viewerPollInterval time.Duration
	// viewerPollChannels は 1 回の poll でサンプリングするチャンネル数です。
	viewerPollChannels int
}

// server は HTTP handler 群が共有する依存関係とメモリ状態を持ちます。
type server struct {
	// cfg は起動時に読み込んだ設定です。
	cfg config
	// client は traQ API / OAuth endpoint を呼ぶ HTTP client です。
	client *http.Client
	// mu は OAuth state と session map を守る mutex です。
	mu sync.Mutex
	// states は OAuth state とその期限を保持します。
	states map[string]time.Time
	// sessions は session ID から token response を引くメモリストアです。
	sessions map[string]tokenResponse
	// state はチャンネル熱量とユーザー現在地を管理します。
	state *stateManager
	// initPayload は demo mode の SSE init で送る JSON です。
	initPayload []byte
}

// channel はサーバ内で扱うチャンネルノードと熱量状態です。
type channel struct {
	// ID は traQ channel ID または demo 用 ID です。
	ID string
	// Name は UI に表示するチャンネル名です。
	Name string
	// ParentID は親チャンネル ID です。
	ParentID string
	// Children は子チャンネル ID の配列です。
	Children []string
	// IslandID はフロントの色分け/島配置に使います。
	IslandID int
	// Depth は root からの深さで、サイズや距離に使います。
	Depth int
	// Score は現在のサーバ側熱量です。
	Score float64
	// LastSyncScore は最後にフロントへ送った熱量です。
	LastSyncScore float64
	// LastSyncTime は最後に減衰/同期した時刻です。
	LastSyncTime time.Time
}

type userState struct {
	UserID         string
	CurrentChannel string
	LastUpdated    time.Time
}

type stateManager struct {
	mu       sync.RWMutex
	channels map[string]*channel
	users    map[string]*userState
}

type initChannel struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	ParentID string   `json:"parentId"`
	Children []string `json:"children"`
	IslandID int      `json:"islandId"`
	Depth    int      `json:"depth"`
}

type initPayload struct {
	Channels map[string]initChannel `json:"channels"`
}

type traqChannelList struct {
	Public []traqChannel `json:"public"`
}

type traqChannel struct {
	ID       string   `json:"id"`
	ParentID *string  `json:"parentId"`
	Archived bool     `json:"archived"`
	Name     string   `json:"name"`
	Children []string `json:"children"`
}

type triggerPayload struct {
	Type string `json:"type"`
	Ch   string `json:"ch,omitempty"`
	Usr  string `json:"usr,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type viewerSnapshotPayload struct {
	TS              int64                  `json:"ts"`
	Total           int                    `json:"total"`
	SampledChannels int                    `json:"sampledChannels"`
	TotalChannels   int                    `json:"totalChannels"`
	Channels        []viewerChannelSummary `json:"channels"`
	Recent          []viewerRow            `json:"recent"`
}

type viewerChannelSummary struct {
	ChannelID   string `json:"channelId"`
	ChannelName string `json:"channelName"`
	Count       int    `json:"count"`
	Monitoring  int    `json:"monitoring"`
	Editing     int    `json:"editing"`
	Stale       int    `json:"stale"`
}

type viewerRow struct {
	UserID      string    `json:"userId"`
	ChannelID   string    `json:"channelId"`
	ChannelName string    `json:"channelName"`
	State       string    `json:"state"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type syncPayload struct {
	TS     int64              `json:"ts"`
	Deltas map[string]float64 `json:"deltas"`
}

type wsEvent struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body"`
}

type wsMessageCreatedBody struct {
	ID       string `json:"id"`
	IsCiting bool   `json:"is_citing"`
}

type wsUserViewStateChangedBody struct {
	ViewStates []wsViewState `json:"view_states"`
}

type wsViewState struct {
	Key            string `json:"key"`
	ChannelID      string `json:"channelId"`
	ChannelIDSnake string `json:"channel_id"`
	State          string `json:"state"`
}

func (s wsViewState) channelID() string {
	if s.ChannelID != "" {
		return s.ChannelID
	}
	return s.ChannelIDSnake
}

type traqMessage struct {
	ChannelID string `json:"channelId"`
}

type channelData struct {
	Channels   []traqChannel
	ChannelIDs map[string]bool
	InitJSON   []byte
}

type traqChannelViewer struct {
	UserID    string    `json:"userId"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type viewerPoller struct {
	mu            sync.Mutex
	channels      []traqChannel
	messageWeight map[string]float64
	maxPerTick    int
}

type weightedChannel struct {
	channel traqChannel
	weight  float64
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
}
