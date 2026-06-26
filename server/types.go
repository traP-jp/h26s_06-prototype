package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// このファイルは、サーバ全体で共有する設定・状態・traQ API 用 DTO をまとめます。
const grandRootID = "grand_root"

type config struct {
	addr               string
	traqBaseURL        string
	clientID           string
	redirectURL        string
	scope              string
	appOrigin          string
	viewerPollInterval time.Duration
	viewerPollChannels int
}

type server struct {
	cfg         config
	client      *http.Client
	mu          sync.Mutex
	states      map[string]time.Time
	sessions    map[string]tokenResponse
	state       *stateManager
	initPayload []byte
}

type channel struct {
	ID            string
	Name          string
	ParentID      string
	Children      []string
	IslandID      int
	Depth         int
	Score         float64
	LastSyncScore float64
	LastSyncTime  time.Time
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
