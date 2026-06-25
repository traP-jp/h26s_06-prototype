# h26s_06 prototype

traQ の投稿・閲覧移動イベントを Go backend で SSE に変換し、Vue frontend で「光の島」として表示する最小プロトタイプです。

## Run

```powershell
go run ./server
```

```powershell
cd client
npm run dev
```

frontend は `http://localhost:5173`、backend は `http://localhost:8080` を使います。

## Modes

- `Demo`: 認証なしでサンプルチャンネルと疑似イベントを流します。
- `OAuth` -> `Live`: `.env` に `TRAQ_CLIENT_ID` を設定すると traQ OAuth でログインし、`GET /api/v3/ws` の `MESSAGE_CREATED` と `USER_VIEWSTATE_CHANGED` を `trigger` イベントに変換します。
  - 接続開始時に `GET /api/v3/channels` を取得し、traQ の実チャンネルツリーを `init` として使います。
  - `MESSAGE_CREATED` は WS body の message id から `GET /api/v3/messages/{messageId}` を取得し、`channelId` を投稿 trigger に使います。
  - `USER_VIEWSTATE_CHANGED` は WS body の `view_states[]` を読み、`key` を匿名化した WS セッション識別子として移動 trigger に使います。

## Environment

```env
TRAQ_CLIENT_ID=
TRAQ_REDIRECT_URL=http://localhost:8080/api/auth/callback
APP_ORIGIN=http://localhost:5173
TRAQ_BASE_URL=https://q.trap.jp
OAUTH_SCOPE=read
SERVER_ADDR=:8080
```
