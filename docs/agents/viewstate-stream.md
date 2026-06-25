# traQ 閲覧状態ストリーム実装メモ

## 目的

全員の traQ チャンネル閲覧状況を、画面とコンソールでわかりやすく確認できるようにする。

## 確認した traQ API

traQ OpenAPI `docs/v3-api.yaml` で以下を確認した。

- `GET /api/v3/ws`
  - WebSocket 通知ストリーム。
  - `USER_VIEWSTATE_CHANGED` は存在する。
  - ただし対象は「変化した WS セッションを含めた、該当ユーザーの WS セッション全て」なので、このイベントだけでは全員分の閲覧変更を購読できない。
- `GET /api/v3/channels/{channelId}/viewers`
  - 指定チャンネルの閲覧者一覧を取得できる。
  - 各 viewer は `userId`, `state`, `updatedAt` を持つ。
- `GET /api/v3/users/me/view-states`
  - 自分の WS セッションごとの閲覧状態を取得できる。
  - 全員分の用途には使わない。

## 採用した方式

リアルタイム WS と polling を併用する。

1. Live 接続開始時に `GET /api/v3/channels` で公開チャンネル一覧を取得する。
2. 自身または祖先がアーカイブ済みのチャンネルは、初期表示、viewer polling、activity trigger の対象から除外する。
3. 既存通り `GET /api/v3/ws` に接続し、`MESSAGE_CREATED` と `USER_VIEWSTATE_CHANGED` を activity trigger として扱う。
4. 全員分の閲覧状況は、公開チャンネルの一部に対して `GET /api/v3/channels/{channelId}/viewers` を定期実行する。
   - 各 tick で最大 `VIEWER_POLL_CHANNELS` 件だけ選ぶ。
   - `MESSAGE_CREATED` が来たチャンネルの重みを上げ、メッセージが多いチャンネルほど選ばれやすくする。
   - 重みは tick ごとに減衰するので、最近活発なチャンネルが優先される。
5. backend は viewer snapshot を `viewers` SSE イベントとして frontend に送る。
6. backend は前回 snapshot と比較し、entered / left / state changed をログ出力する。

`USER_VIEWSTATE_CHANGED` は全員分を保証しないため、全体表示の正は channel viewers polling 側に置いている。ただし API リクエスト数を抑えるため、現在は全件ではなく重み付きサンプリングで取得する。

## Backend

主な実装ファイル: `server/main.go`

- `fetchChannelData`
  - `GET /api/v3/channels` を取得し、自身または祖先がアーカイブ済みのチャンネルを除外してから 3D 表示用 init payload と polling 用チャンネル一覧を作る。
- `streamTraqTriggers`
  - `GET /api/v3/ws` に接続し、既存の activity trigger を流す。
- `streamViewerSnapshots`
  - `VIEWER_POLL_INTERVAL` ごとに viewer snapshot を作る。
- `viewerPoller`
  - 公開チャンネルから weighted random sampling without replacement で polling 対象を選ぶ。
  - base weight は `1`。
  - `MESSAGE_CREATED` が取れたチャンネルは重みを加算する。
  - tick ごとに message weight を減衰させる。
- `fetchViewerSnapshot`
  - `viewerPoller` が選んだチャンネルに対して `fetchChannelViewers` を並列実行する。
  - 同時リクエスト数は 8 に制限している。
  - チャンネル別集計と最近更新された viewer 行を作る。
- `logViewerChanges`
  - 前回 snapshot との差分を backend console に出す。
  - サンプルしていないチャンネルは退出判定しない。
- `withCORS`
  - 開発時に `localhost` と `127.0.0.1` の両方から使えるよう、loopback host は同じ origin として許可する。

SSE イベント:

- `init`: チャンネルツリー
- `status`: 接続状態
- `trigger`: 投稿・閲覧移動の可視化 trigger
- `sync`: heat score 同期
- `viewers`: 全体閲覧状況 snapshot
- `stream-error`: stream エラー

## Frontend

主な実装ファイル: `client/src/App.vue`, `client/src/style.css`

`viewers` SSE を受け取って以下を表示する。

- `Viewers`: 現在 viewer として取得できた合計数
- `閲覧中チャンネル`: viewer 数の多いチャンネル上位
- `最近の閲覧`: `updatedAt` が新しい viewer 行

同じ snapshot の最近行は `console.table` にも出す。

state 表示:

- `monitoring`: 閲覧中
- `editing`: 入力中
- `stale_viewing`: 過去ログ
- `none`: 非表示

## 設定

`.env` で設定する。

```env
TRAQ_CLIENT_ID=
TRAQ_REDIRECT_URL=http://localhost:8080/api/auth/callback
APP_ORIGIN=http://localhost:5173
TRAQ_BASE_URL=https://q.trap.jp
OAUTH_SCOPE=read
SERVER_ADDR=:8080
VIEWER_POLL_INTERVAL=20s
VIEWER_POLL_CHANNELS=40
```

`VIEWER_POLL_INTERVAL` は Go の duration 形式。例: `10s`, `30s`, `1m`。

`VIEWER_POLL_CHANNELS` は 1回の polling で `/viewers` を取得するチャンネル数。

1分あたりの viewer API リクエスト数は、おおよそ `60 / VIEWER_POLL_INTERVAL秒 * VIEWER_POLL_CHANNELS`。

例: `VIEWER_POLL_INTERVAL=20s`, `VIEWER_POLL_CHANNELS=40` の場合は約 120 req/min。

短くしすぎたり増やしすぎたりすると API リクエストが増えるため、必要な精度に応じて調整する。

## 動かし方

Backend:

```sh
go run ./server
```

Frontend:

```sh
cd client
npm run dev
```

開発環境で Go build cache がホーム配下に書けない場合:

```sh
GOCACHE=/tmp/go-build go run ./server
```

ブラウザ:

- `http://localhost:5173/`
- `http://127.0.0.1:5173/`

どちらでも backend CORS は通る。

## 制限

- 全員分の閲覧変化は traQ WS だけでは購読できないため、全体表示は polling による疑似リアルタイム。
- polling は重み付きサンプリングなので、低活動チャンネルの閲覧状況は更新頻度が低くなる。
- viewer snapshot は polling 間隔内の一瞬の入退室を取り逃がす可能性がある。
- 画面の `Viewers` は「最近サンプルされたチャンネルから分かっている viewer 数」であり、全チャンネルの厳密な現在値ではない。
- userId は画面では短縮表示しているが、backend/frontend payload にはそのまま含めている。
