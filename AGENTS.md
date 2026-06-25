# Project Notes

## Purpose

Prototype app that authenticates with traQ OAuth, connects to traQ `GET /api/v3/ws` from a Go backend, filters `USER_VIEWSTATE_CHANGED` WebSocket events, and streams them to a Vue frontend over SSE.

## Commands

- Backend: `go run ./backend`
- Frontend: `cd frontend && npm run dev`
- Backend formatting: `gofmt -w backend/main.go`

## Configuration

Copy `.env.example` to `.env` and set `TRAQ_CLIENT_ID`. The default redirect URL is `http://localhost:8080/api/auth/callback`, so the traQ OAuth client must register the same URL.
