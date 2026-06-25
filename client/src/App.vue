<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { createTraqScene } from './graphScene.js'

const apiBase = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080'

const stageRef = ref(null)
const authenticated = ref(false)
const oauthConfigured = ref(false)
const connected = ref(false)
const status = ref('起動中')
const streamMode = ref('demo')
const nodeCount = ref(0)
const rippleCount = ref(0)
const beamCount = ref(0)
const activeChannels = ref([])
const hoveredChannel = ref(null)
const events = ref([])
const viewerTotal = ref(0)
const viewerChannels = ref([])
const viewerRecent = ref([])
const viewerUpdatedAt = ref('')
const sampledChannels = ref(0)
const totalChannels = ref(0)

let source = null
let topologyScene = null

const latestEvents = computed(() => events.value.slice(0, 8))
const visibleViewerChannels = computed(() => viewerChannels.value.slice(0, 8))
const visibleViewerRecent = computed(() => viewerRecent.value.slice(0, 8))

async function refreshAuth() {
  try {
    const response = await fetch(`${apiBase}/api/me`, { credentials: 'include' })
    if (!response.ok) {
      status.value = `/api/me ${response.status}`
      return
    }
    const body = await response.json()
    authenticated.value = body.authenticated
    oauthConfigured.value = body.oauthConfigured
    status.value = authenticated.value ? '認証済み' : 'デモ待機'
  } catch (error) {
    status.value = `backend 未接続: ${error.message}`
  }
}

function login() {
  window.location.href = `${apiBase}/api/auth/login`
}

async function logout() {
  disconnect()
  await fetch(`${apiBase}/api/auth/logout`, { method: 'POST', credentials: 'include' })
  authenticated.value = false
  status.value = 'ログアウト済み'
}

function connect(mode) {
  disconnect()
  streamMode.value = mode
  status.value = mode === 'demo' ? 'デモ接続中' : 'traQ 接続中'
  const suffix = mode === 'demo' ? '?demo=1' : ''
  source = new EventSource(`${apiBase}/api/events${suffix}`, { withCredentials: true })

  source.addEventListener('init', (event) => {
    const payload = JSON.parse(event.data)
    topologyScene?.setChannels(payload.channels)
    connected.value = true
    status.value = mode === 'demo' ? 'デモ受信中' : 'traQ 受信中'
  })

  source.addEventListener('status', (event) => {
    connected.value = true
    status.value = JSON.parse(event.data).status
  })

  source.addEventListener('trigger', (event) => {
    const payload = JSON.parse(event.data)
    topologyScene?.trigger(payload)
    rememberEvent(payload)
  })

  source.addEventListener('sync', (event) => {
    const payload = JSON.parse(event.data)
    topologyScene?.sync(payload.deltas)
  })

  source.addEventListener('viewers', (event) => {
    const payload = JSON.parse(event.data)
    viewerTotal.value = payload.total ?? 0
    viewerChannels.value = payload.channels ?? []
    viewerRecent.value = payload.recent ?? []
    sampledChannels.value = payload.sampledChannels ?? 0
    totalChannels.value = payload.totalChannels ?? 0
    viewerUpdatedAt.value = new Date((payload.ts ?? Date.now() / 1000) * 1000).toLocaleTimeString()
    console.table(
      viewerRecent.value.map((row) => ({
        user: shortUser(row.userId),
        channel: row.channelName,
        state: stateLabel(row.state),
        updatedAt: formatTime(row.updatedAt),
      })),
    )
  })

  source.addEventListener('stream-error', (event) => {
    connected.value = false
    status.value = event.data ? JSON.parse(event.data).error : 'SSE エラー'
  })

  source.onerror = () => {
    connected.value = false
    status.value = '再接続待機中'
  }
}

function disconnect() {
  if (source) {
    source.close()
    source = null
  }
  connected.value = false
}

function stateLabel(state) {
  switch (state) {
    case 'editing':
      return '入力中'
    case 'monitoring':
      return '閲覧中'
    case 'stale_viewing':
      return '過去ログ'
    case 'none':
      return '非表示'
    default:
      return state || '-'
  }
}

function shortUser(userId) {
  if (!userId) return '-'
  return userId.length > 12 ? `${userId.slice(0, 8)}...` : userId
}

function formatTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '-'
  return date.toLocaleTimeString()
}

function rememberEvent(payload) {
  const label =
    payload.type === 'msg'
      ? `msg -> ${channelLabel(payload.ch)}`
      : `mov -> ${channelLabel(payload.to)}`
  events.value.unshift({ id: makeId(), label, at: new Date().toLocaleTimeString() })
  events.value = events.value.slice(0, 16)
}

function channelLabel(id) {
  const active = activeChannels.value.find((node) => node.id === id)
  if (active) return active.name
  if (hoveredChannel.value?.id === id) return hoveredChannel.value.name
  return id
}

function updateStats(stats) {
  nodeCount.value = stats.nodes
  rippleCount.value = stats.ripples
  beamCount.value = stats.beams
  activeChannels.value = stats.activeChannels
}

function makeId() {
  return crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`
}

onMounted(async () => {
  if (window.location.pathname === '/oauth/callback' && window.location.search.includes('code=')) {
    window.location.replace(`${apiBase}/api/auth/callback${window.location.search}`)
    return
  }

  topologyScene = createTraqScene(stageRef.value, {
    onStats: updateStats,
    onHover: (node) => {
      hoveredChannel.value = node
    },
    onContextLost: () => {
      status.value = 'WebGL コンテキスト消失'
      connected.value = false
    },
  })

  await refreshAuth()
  connect('demo')
})

onBeforeUnmount(() => {
  disconnect()
  topologyScene?.dispose()
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
