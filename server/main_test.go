package main

import (
	"encoding/json"
	"testing"
)

func TestFilterActiveTraqChannelsSkipsArchivedAndEmptyID(t *testing.T) {
	archivedParent := "archived-parent"
	channels := []traqChannel{
		{ID: "active", Name: "active"},
		{ID: "archived", Name: "archived", Archived: true},
		{ID: archivedParent, Name: "archived-parent", Archived: true},
		{ID: "child-of-archived", ParentID: &archivedParent, Name: "child-of-archived"},
		{Name: "empty"},
	}

	active := filterActiveTraqChannels(channels)

	if len(active) != 1 {
		t.Fatalf("len(active) = %d, want 1", len(active))
	}
	if active[0].ID != "active" {
		t.Fatalf("active[0].ID = %q, want active", active[0].ID)
	}
}

func TestBuildTraqInitPayloadSkipsArchivedChannels(t *testing.T) {
	activeParent := "active-parent"
	archivedParent := "archived-parent"
	channels := []traqChannel{
		{ID: activeParent, Name: "active-parent"},
		{ID: "active-child", ParentID: &activeParent, Name: "active-child"},
		{ID: archivedParent, Name: "archived-parent", Archived: true},
		{ID: "child-of-archived", ParentID: &archivedParent, Name: "child-of-archived"},
	}

	active := filterActiveTraqChannels(channels)
	rawPayload, err := buildTraqInitPayload(active)
	if err != nil {
		t.Fatalf("buildTraqInitPayload returned error: %v", err)
	}

	var payload initPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	if _, ok := payload.Channels["archived-parent"]; ok {
		t.Fatal("archived parent was included in init payload")
	}
	if _, ok := payload.Channels["child-of-archived"]; ok {
		t.Fatal("child of archived parent was included in init payload")
	}
	if _, ok := payload.Channels["active-parent"]; !ok {
		t.Fatal("active parent was not included in init payload")
	}
	if got := payload.Channels["active-child"].ParentID; got != activeParent {
		t.Fatalf("active-child parent = %q, want %q", got, activeParent)
	}
}

func TestNewViewerPollerSkipsArchivedChannels(t *testing.T) {
	archivedParent := "archived-parent"
	activeChannels := filterActiveTraqChannels([]traqChannel{
		{ID: "active", Name: "active"},
		{ID: "archived", Name: "archived", Archived: true},
		{ID: archivedParent, Name: "archived-parent", Archived: true},
		{ID: "child-of-archived", ParentID: &archivedParent, Name: "child-of-archived"},
	})
	poller := newViewerPoller(activeChannels, 10)

	if len(poller.channels) != 1 {
		t.Fatalf("len(poller.channels) = %d, want 1", len(poller.channels))
	}
	if poller.channels[0].ID != "active" {
		t.Fatalf("poller.channels[0].ID = %q, want active", poller.channels[0].ID)
	}
}

func TestIsTriggerForActiveChannel(t *testing.T) {
	activeIDs := map[string]bool{"active": true}

	tests := []struct {
		name    string
		trigger triggerPayload
		want    bool
	}{
		{name: "active message", trigger: triggerPayload{Type: "msg", Ch: "active"}, want: true},
		{name: "archived message", trigger: triggerPayload{Type: "msg", Ch: "archived"}, want: false},
		{name: "active movement", trigger: triggerPayload{Type: "mov", To: "active"}, want: true},
		{name: "archived movement", trigger: triggerPayload{Type: "mov", To: "archived"}, want: false},
		{name: "unknown trigger type", trigger: triggerPayload{Type: "other", Ch: "active"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTriggerForActiveChannel(tt.trigger, activeIDs); got != tt.want {
				t.Fatalf("isTriggerForActiveChannel() = %t, want %t", got, tt.want)
			}
		})
	}
}
