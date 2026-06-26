<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useSceneStats } from './composables/useSceneStats.js'
import { useTraqAuth } from './composables/useTraqAuth.js'
import { useTraqEventStream } from './composables/useTraqEventStream.js'
import { createTraqScene } from './graphScene.js'
import { formatTime, shortUser, stateLabel } from './utils/formatters.js'

const apiBase = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080'

const stageRef = ref(null)
const topologyScene = ref(null)

// Three.js シーンの統計とホバー状態を UI へ橋渡しします。
const {
  nodeCount,
  rippleCount,
  beamCount,
  activeChannels,
  hoveredChannel,
  updateStats,
  updateHover,
} = useSceneStats()

// SSE から届くイベント、閲覧者情報、接続状態をまとめて扱います。
const {
  connected,
  status,
  streamMode,
  events,
  viewerTotal,
  viewerChannels,
  viewerRecent,
  viewerUpdatedAt,
  sampledChannels,
  totalChannels,
  connect,
  disconnect,
  markContextLost,
} = useTraqEventStream(apiBase, topologyScene, activeChannels, hoveredChannel)

// OAuth の状態管理を独立させ、ログアウト時は SSE も確実に閉じます。
const {
  authenticated,
  oauthConfigured,
  authStatus,
  refreshAuth,
  login,
  logout: logoutAuth,
} = useTraqAuth(apiBase, disconnect)

const latestEvents = computed(() => events.value.slice(0, 8))
const visibleViewerChannels = computed(() => viewerChannels.value.slice(0, 8))
const visibleViewerRecent = computed(() => viewerRecent.value.slice(0, 8))

async function logout() {
  await logoutAuth()
  status.value = authStatus.value
}

onMounted(async () => {
  if (window.location.pathname === '/oauth/callback' && window.location.search.includes('code=')) {
    window.location.replace(`${apiBase}/api/auth/callback${window.location.search}`)
    return
  }

  topologyScene.value = createTraqScene(stageRef.value, {
    onStats: updateStats,
    onHover: updateHover,
    onContextLost: markContextLost,
  })

  await refreshAuth()
  status.value = authStatus.value
  connect('demo')
})

onBeforeUnmount(() => {
  disconnect()
  topologyScene.value?.dispose()
})
</script>

<template>
  <main class="appShell">
    <section ref="stageRef" class="stage" aria-label="traQ activity topology" />

    <aside class="hud" @pointerdown.stop @wheel.stop>
      <header>
        <div>
          <p class="eyebrow">traQ activity prototype</p>
          <h1>Light Islands</h1>
        </div>
        <span class="status" :class="{ on: connected }">{{ status }}</span>
      </header>

      <div class="actions">
        <button type="button" :class="{ active: streamMode === 'demo' && connected }" @click="connect('demo')">
          Demo
        </button>
        <button
          type="button"
          :disabled="!authenticated"
          :class="{ active: streamMode === 'live' && connected }"
          @click="connect('live')"
        >
          Live
        </button>
        <button v-if="!authenticated" type="button" :disabled="!oauthConfigured" @click="login">
          OAuth
        </button>
        <button v-else type="button" @click="logout">Logout</button>
      </div>

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

      <section v-if="hoveredChannel" class="focusPanel">
        <span class="swatch" :style="{ background: hoveredChannel.color }" />
        <div>
          <h2>{{ hoveredChannel.name }}</h2>
          <p>depth {{ hoveredChannel.depth }} / {{ hoveredChannel.score.toFixed(1) }}</p>
        </div>
      </section>

      <section class="panel">
        <h2>Heat</h2>
        <ol>
          <li v-for="node in activeChannels" :key="node.id">
            <span :style="{ color: node.color }">{{ node.name }}</span>
            <meter min="0" max="100" :value="node.score" />
          </li>
        </ol>
      </section>

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
