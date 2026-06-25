# Project Notes

## Purpose

Prototype app that authenticates with traQ OAuth, connects to traQ `GET /api/v3/ws` from a Go backend, filters `USER_VIEWSTATE_CHANGED` WebSocket events, polls channel viewers in live mode, and streams activity to a Vue frontend over SSE.

## Commands

- Backend: `go run ./server`
- Frontend: `cd client && npm run dev`
- Backend formatting: `gofmt -w server/main.go`

## Configuration

Copy `.env.example` to `.env` and set `TRAQ_CLIENT_ID`. The default redirect URL is `http://localhost:8080/api/auth/callback`, so the traQ OAuth client must register the same URL.

`VIEWER_POLL_INTERVAL` controls how often live mode polls `GET /api/v3/channels/{channelId}/viewers`. Default: `20s`.

`VIEWER_POLL_CHANNELS` controls how many public channels are sampled per viewer poll. Default: `40`. Channels with recent `MESSAGE_CREATED` events are sampled more often.

## Notes

- traQ viewstate streaming implementation: `docs/agents/viewstate-stream.md`
