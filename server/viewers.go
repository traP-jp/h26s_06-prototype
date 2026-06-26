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
	activeChannels := make([]traqChannel, 0, len(channels))
	for _, ch := range channels {
		if ch.ID != "" && !ch.Archived {
			activeChannels = append(activeChannels, ch)
		}
	}
	if maxPerTick <= 0 || maxPerTick > len(activeChannels) {
		maxPerTick = len(activeChannels)
	}
	return &viewerPoller{
		channels:      activeChannels,
		messageWeight: map[string]float64{},
		maxPerTick:    maxPerTick,
	}
}

func (p *viewerPoller) noteMessage(channelID string) {
	if p == nil || channelID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messageWeight[channelID] = math.Min(120, p.messageWeight[channelID]+12)
}

func (p *viewerPoller) sampleChannels() []traqChannel {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.channels) <= p.maxPerTick {
		return append([]traqChannel(nil), p.channels...)
	}

	candidates := make([]weightedChannel, 0, len(p.channels))
	for _, ch := range p.channels {
		messageWeight := p.messageWeight[ch.ID]
		candidates = append(candidates, weightedChannel{
			channel: ch,
			weight:  1 + messageWeight,
		})
		if messageWeight < 0.05 {
			delete(p.messageWeight, ch.ID)
		} else {
			p.messageWeight[ch.ID] = messageWeight * 0.82
		}
	}

	selected := make([]traqChannel, 0, p.maxPerTick)
	for len(selected) < p.maxPerTick && len(candidates) > 0 {
		totalWeight := 0.0
		for _, candidate := range candidates {
			totalWeight += candidate.weight
		}
		pick := rand.Float64() * totalWeight
		selectedIndex := 0
		for i, candidate := range candidates {
			pick -= candidate.weight
			if pick <= 0 {
				selectedIndex = i
				break
			}
		}
		selected = append(selected, candidates[selectedIndex].channel)
		candidates = append(candidates[:selectedIndex], candidates[selectedIndex+1:]...)
	}

	return selected
}

func (s *server) streamViewerSnapshots(ctx context.Context, accessToken string, poller *viewerPoller) <-chan viewerSnapshotPayload {
	out := make(chan viewerSnapshotPayload)

	go func() {
		defer close(out)

		ticker := time.NewTicker(s.cfg.viewerPollInterval)
		defer ticker.Stop()

		previous := map[string]viewerRow{}
		for {
			snapshot, current, sampledChannelIDs, err := s.fetchViewerSnapshot(ctx, accessToken, poller)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("viewer snapshot skipped: %v", err)
				}
			} else {
				logViewerChanges(filterViewerRows(previous, sampledChannelIDs), current)
				mergeViewerRows(previous, current, sampledChannelIDs)
				snapshot.Total = len(previous)
				select {
				case out <- snapshot:
				case <-ctx.Done():
					return
				}
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	return out
}

func (s *server) fetchViewerSnapshot(ctx context.Context, accessToken string, poller *viewerPoller) (viewerSnapshotPayload, map[string]viewerRow, map[string]bool, error) {
	type result struct {
		channel traqChannel
		viewers []traqChannelViewer
		err     error
	}

	activeChannels := poller.sampleChannels()
	sampledChannelIDs := make(map[string]bool, len(activeChannels))
	for _, ch := range activeChannels {
		sampledChannelIDs[ch.ID] = true
	}

	sem := make(chan struct{}, 8)
	results := make(chan result, len(activeChannels))
	var wg sync.WaitGroup
	for _, ch := range activeChannels {
		ch := ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- result{channel: ch, err: ctx.Err()}
				return
			}
			viewers, err := s.fetchChannelViewers(ctx, accessToken, ch.ID)
			results <- result{channel: ch, viewers: viewers, err: err}
		}()
	}

	wg.Wait()
	close(results)

	rows := make([]viewerRow, 0)
	summaries := make([]viewerChannelSummary, 0)
	current := map[string]viewerRow{}
	var firstErr error
	for res := range results {
		if res.err != nil {
			if firstErr == nil {
				firstErr = res.err
			}
			continue
		}
		if len(res.viewers) == 0 {
			continue
		}

		summary := viewerChannelSummary{
			ChannelID:   res.channel.ID,
			ChannelName: res.channel.Name,
			Count:       len(res.viewers),
		}
		for _, viewer := range res.viewers {
			switch viewer.State {
			case "editing":
				summary.Editing++
			case "monitoring":
				summary.Monitoring++
			case "stale_viewing":
				summary.Stale++
			}
			row := viewerRow{
				UserID:      viewer.UserID,
				ChannelID:   res.channel.ID,
				ChannelName: res.channel.Name,
				State:       viewer.State,
				UpdatedAt:   viewer.UpdatedAt,
			}
			rows = append(rows, row)
			current[viewerKey(row)] = row
		}
		summaries = append(summaries, summary)
	}
	if firstErr != nil && len(rows) == 0 {
		return viewerSnapshotPayload{}, nil, nil, firstErr
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Count == summaries[j].Count {
			return summaries[i].ChannelName < summaries[j].ChannelName
		}
		return summaries[i].Count > summaries[j].Count
	})
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	if len(summaries) > 12 {
		summaries = summaries[:12]
	}
	if len(rows) > 24 {
		rows = rows[:24]
	}

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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.traqBaseURL+"/api/v3/channels/"+url.PathEscape(channelID)+"/viewers", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("channel viewers endpoint returned %s for %s: %s", resp.Status, channelID, strings.TrimSpace(string(body)))
	}

	var viewers []traqChannelViewer
	if err := json.Unmarshal(body, &viewers); err != nil {
		return nil, err
	}
	return viewers, nil
}

func filterViewerRows(rows map[string]viewerRow, channelIDs map[string]bool) map[string]viewerRow {
	filtered := make(map[string]viewerRow)
	for key, row := range rows {
		if channelIDs[row.ChannelID] {
			filtered[key] = row
		}
	}
	return filtered
}

func mergeViewerRows(rows map[string]viewerRow, current map[string]viewerRow, sampledChannelIDs map[string]bool) {
	for key, row := range rows {
		if sampledChannelIDs[row.ChannelID] {
			delete(rows, key)
		}
	}
	for key, row := range current {
		rows[key] = row
	}
}

func logViewerChanges(previous, current map[string]viewerRow) {
	if previous == nil {
		return
	}
	for key, row := range current {
		prev, ok := previous[key]
		if !ok {
			log.Printf("viewer entered: user=%s channel=%s state=%s", row.UserID, row.ChannelName, row.State)
			continue
		}
		if prev.State != row.State {
			log.Printf("viewer state changed: user=%s channel=%s %s->%s", row.UserID, row.ChannelName, prev.State, row.State)
		}
	}
	for key, row := range previous {
		if _, ok := current[key]; !ok {
			log.Printf("viewer left: user=%s channel=%s state=%s", row.UserID, row.ChannelName, row.State)
		}
	}
}

func viewerKey(row viewerRow) string {
	return row.UserID + "|" + row.ChannelID
}
