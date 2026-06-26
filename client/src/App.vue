<script setup>
// Vue のライフサイクルと算出プロパティを使い、画面全体の状態を組み立てます。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
// Three.js シーンから届く統計値を Vue の ref として扱うための composable です。
import { useSceneStats } from './composables/useSceneStats.js'
// OAuth ログイン状態の確認・ログイン・ログアウトをまとめた composable です。
import { useTraqAuth } from './composables/useTraqAuth.js'
// SSE 接続と受信イベントの状態反映をまとめた composable です。
import { useTraqEventStream } from './composables/useTraqEventStream.js'
// Three.js の可視化シーンを生成する入口です。
import { createTraqScene } from './graphScene.js'
// 表示用の短縮文字列や状態名変換をテンプレートから使います。
import { formatTime, shortUser, stateLabel } from './utils/formatters.js'

// Vite の環境変数があればそれを使い、未設定ならローカル backend を見るようにします。
const apiBase = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080'

// Three.js の canvas を差し込む DOM 要素を受け取るための参照です。
const stageRef = ref(null)
// createTraqScene が返すシーンインスタンスを保持し、SSE 側から操作できるようにします。
const topologyScene = ref(null)

// Three.js シーンの統計とホバー状態を UI へ橋渡しします。
const {
  // 現在描画対象になっているチャンネルノード数です。
  nodeCount,
  // 画面上で生きている波紋エフェクト数です。
  rippleCount,
  // 画面上で生きている移動ビーム数です。
  beamCount,
  // 熱量が高い上位チャンネルを Heat パネルへ表示します。
  activeChannels,
  // マウスホバー中のチャンネルを focusPanel へ表示します。
  hoveredChannel,
  // Three.js 側から届いた統計値で上記 ref を更新します。
  updateStats,
  // Three.js 側から届いたホバー対象で hoveredChannel を更新します。
  updateHover,
} = useSceneStats()

// SSE から届くイベント、閲覧者情報、接続状態をまとめて扱います。
const {
  // SSE が現在つながっているかをステータスバッジに反映します。
  connected,
  // HUD のステータステキストに表示する文字列です。
  status,
  // demo/live のどちらで接続中かをボタンの active 表示に使います。
  streamMode,
  // 最近受け取った msg/mov イベントの履歴です。
  events,
  // viewer snapshot に含まれる閲覧者の合計です。
  viewerTotal,
  // 閲覧者が多いチャンネルの集計です。
  viewerChannels,
  // 最近更新された閲覧者行です。
  viewerRecent,
  // viewer snapshot を最後に受け取った時刻表示です。
  viewerUpdatedAt,
  // 今回の viewer poll でサンプリングしたチャンネル数です。
  sampledChannels,
  // traQ から取得した live 対象チャンネルの総数です。
  totalChannels,
  // demo/live の SSE 接続を開始します。
  connect,
  // 既存の SSE 接続を閉じます。
  disconnect,
  // WebGL context lost 時に UI 状態をエラー表示へ変えます。
  markContextLost,
} = useTraqEventStream(apiBase, topologyScene, activeChannels, hoveredChannel)

// OAuth の状態管理を独立させ、ログアウト時は SSE も確実に閉じます。
const {
  // traQ OAuth 済みなら live ボタンを押せるようにします。
  authenticated,
  // backend に client id が設定されていない場合は OAuth ボタンを無効にします。
  oauthConfigured,
  // 認証状態確認後のステータス文字列です。
  authStatus,
  // /api/me を呼び、認証済みかどうかを確認します。
  refreshAuth,
  // traQ OAuth のログイン開始 URL へ遷移します。
  login,
  // backend のセッションを破棄します。ここでは名前を変えて薄い wrapper から呼びます。
  logout: logoutAuth,
} = useTraqAuth(apiBase, disconnect)

// Events パネルは最新 8 件だけを見せ、リストが伸びすぎないようにします。
const latestEvents = computed(() => events.value.slice(0, 8))
// viewer channel パネルも上位 8 件に絞って HUD をコンパクトに保ちます。
const visibleViewerChannels = computed(() => viewerChannels.value.slice(0, 8))
// 最近の閲覧者行も最大 8 件にして、スクロール量を抑えます。
const visibleViewerRecent = computed(() => viewerRecent.value.slice(0, 8))

async function logout() {
  // logoutAuth 内で SSE は閉じられ、backend のセッション Cookie も期限切れになります。
  await logoutAuth()
  // 認証 composable が持つ「ログアウト済み」表示を、画面の status に反映します。
  status.value = authStatus.value
}

onMounted(async () => {
  // フロントが /oauth/callback で受けた code は backend callback へ渡して token 交換させます。
  if (window.location.pathname === '/oauth/callback' && window.location.search.includes('code=')) {
    window.location.replace(`${apiBase}/api/auth/callback${window.location.search}`)
    return
  }

  // DOM ができてから Three.js シーンを作り、stageRef の中に canvas を差し込みます。
  topologyScene.value = createTraqScene(stageRef.value, {
    // フレームごとの統計値を Vue の ref に反映します。
    onStats: updateStats,
    // レイキャストで見つかったホバー中ノードを Vue の ref に反映します。
    onHover: updateHover,
    // WebGL context lost を UI の接続状態に反映します。
    onContextLost: markContextLost,
  })

  // 初期表示時にログイン済みか確認し、OAuth/Live ボタンの状態を決めます。
  await refreshAuth()
  // 認証確認結果を起動直後の status として一度表示します。
  status.value = authStatus.value
  // 何もしなくても動きが見えるよう、初期状態では demo SSE へ接続します。
  connect('demo')
})

onBeforeUnmount(() => {
  // Vue コンポーネントが破棄されるとき、SSE 接続を明示的に閉じます。
  disconnect()
  // Three.js の event listener / geometry / material / renderer を解放します。
  topologyScene.value?.dispose()
})
</script>

<template>
  <!-- 画面全体は canvas と HUD を重ねる構成です。 -->
  <main class="appShell">
    <!-- Three.js がここへ canvas を追加し、全画面の可視化を描画します。 -->
    <section ref="stageRef" class="stage" aria-label="traQ activity topology" />

    <!-- HUD 上でドラッグやホイールしても OrbitControls へイベントが抜けないように止めます。 -->
    <aside class="hud" @pointerdown.stop @wheel.stop>
      <header>
        <div>
          <p class="eyebrow">traQ activity prototype</p>
          <h1>Light Islands</h1>
        </div>
        <!-- connected=true のときだけ緑色の接続状態として見せます。 -->
        <span class="status" :class="{ on: connected }">{{ status }}</span>
      </header>

      <!-- demo/live/OAuth/logout の主要操作をまとめます。 -->
      <div class="actions">
        <!-- demo は未認証でも使えるため常に押せます。 -->
        <button type="button" :class="{ active: streamMode === 'demo' && connected }" @click="connect('demo')">
          Demo
        </button>
        <!-- live は OAuth 済みのときだけ traQ API を呼べるので有効化します。 -->
        <button
          type="button"
          :disabled="!authenticated"
          :class="{ active: streamMode === 'live' && connected }"
          @click="connect('live')"
        >
          Live
        </button>
        <!-- 未認証なら OAuth 開始、認証済みなら logout を表示します。 -->
        <button v-if="!authenticated" type="button" :disabled="!oauthConfigured" @click="login">
          OAuth
        </button>
        <button v-else type="button" @click="logout">Logout</button>
      </div>

      <!-- シーン内の現在値を 4 つの短い指標として表示します。 -->
      <dl class="metrics">
        <div>
          <dt>Nodes</dt>
          <dd>{{ nodeCount }}</dd>
        </div>
        <div>
          <dt>Ripples</dt>
          <dd>{{ rippleCount }}</dd>
        </div>
        <div>
          <dt>Beams</dt>
          <dd>{{ beamCount }}</dd>
        </div>
        <div>
          <dt>Viewers</dt>
          <dd>{{ viewerTotal }}</dd>
        </div>
      </dl>

      <!-- ノードにホバーしている間だけ、対象チャンネルの詳細を出します。 -->
      <section v-if="hoveredChannel" class="focusPanel">
        <span class="swatch" :style="{ background: hoveredChannel.color }" />
        <div>
          <h2>{{ hoveredChannel.name }}</h2>
          <p>depth {{ hoveredChannel.depth }} / {{ hoveredChannel.score.toFixed(1) }}</p>
        </div>
      </section>

      <!-- 熱量が高いチャンネルを meter で見せます。 -->
      <section class="panel">
        <h2>Heat</h2>
        <ol>
          <li v-for="node in activeChannels" :key="node.id">
            <span :style="{ color: node.color }">{{ node.name }}</span>
            <meter min="0" max="100" :value="node.score" />
          </li>
        </ol>
      </section>

      <!-- live mode の viewer snapshot から、閲覧者の多いチャンネルを表示します。 -->
      <section class="panel">
        <h2>
          閲覧中チャンネル
          <small v-if="viewerUpdatedAt">({{ viewerUpdatedAt }} / sample {{ sampledChannels }} of {{ totalChannels }})</small>
        </h2>
        <ol v-if="visibleViewerChannels.length">
          <li v-for="channel in visibleViewerChannels" :key="channel.channelId" class="viewerChannel">
            <span>{{ channel.channelName }}</span>
            <strong>{{ channel.count }}</strong>
            <small>
              閲覧 {{ channel.monitoring }} / 入力 {{ channel.editing }} / 過去 {{ channel.stale }}
            </small>
          </li>
        </ol>
        <p v-else class="emptyText">live 接続後に表示します</p>
      </section>

      <!-- viewer snapshot の recent 行を、人とチャンネルと状態で表示します。 -->
      <section class="panel">
        <h2>最近の閲覧</h2>
        <ul v-if="visibleViewerRecent.length" class="viewerList">
          <li v-for="row in visibleViewerRecent" :key="`${row.userId}-${row.channelId}-${row.updatedAt}`">
            <time>{{ formatTime(row.updatedAt) }}</time>
            <span>{{ shortUser(row.userId) }} / {{ row.channelName }}</span>
            <em>{{ stateLabel(row.state) }}</em>
          </li>
        </ul>
        <p v-else class="emptyText">viewer snapshot を待機中</p>
      </section>

      <!-- SSE trigger の受信履歴を短く表示します。 -->
      <section class="panel">
        <h2>Events</h2>
        <ul>
          <li v-for="event in latestEvents" :key="event.id">
            <time>{{ event.at }}</time>
            <span>{{ event.label }}</span>
          </li>
        </ul>
      </section>
    </aside>
  </main>
</template>
