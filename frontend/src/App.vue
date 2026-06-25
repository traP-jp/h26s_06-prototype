<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const apiBase = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080'
const authenticated = ref(false)
const connected = ref(false)
const status = ref('未接続')
const events = ref([])
let source = null

const newestFirst = computed(() => [...events.value].reverse())

async function refreshAuth() {
  status.value = '認証確認中'
  try {
    const response = await fetch(`${apiBase}/api/me`, { credentials: 'include' })
    if (!response.ok) {
      status.value = `/api/me ${response.status}`
      return
    }
    const body = await response.json()
    authenticated.value = body.authenticated
    status.value = authenticated.value ? '認証済み' : '未認証'
    if (authenticated.value) {
      connect()
    }
  } catch (error) {
    status.value = `認証確認失敗: ${error.message}`
  }
}

function login() {
  window.location.href = `${apiBase}/api/auth/login`
}

async function logout() {
  disconnect()
  await fetch(`${apiBase}/api/auth/logout`, { method: 'POST', credentials: 'include' })
  authenticated.value = false
  events.value = []
  status.value = 'ログアウト済み'
}

function connect() {
  if (source) {
    return
  }
  status.value = '接続中'
  source = new EventSource(`${apiBase}/api/events`, { withCredentials: true })

  source.addEventListener('status', (event) => {
    connected.value = true
    status.value = JSON.parse(event.data).status
  })

  source.addEventListener('USER_VIEWSTATE_CHANGED', (event) => {
    connected.value = true
    status.value = '受信中'
    events.value.push({
      id: crypto.randomUUID(),
      receivedAt: new Date().toISOString(),
      payload: JSON.parse(event.data),
      raw: event.data,
    })
  })

  source.addEventListener('stream-error', (event) => {
    connected.value = false
    status.value = event.data ? JSON.parse(event.data).error : 'SSE 接続エラー'
  })

  source.onerror = () => {
    connected.value = false
    status.value = 'SSE 再接続待機中'
  }
}

function disconnect() {
  if (source) {
    source.close()
    source = null
  }
  connected.value = false
}

function clearEvents() {
  events.value = []
}

onMounted(() => {
  if (window.location.pathname === '/oauth/callback' && window.location.search.includes('code=')) {
    status.value = 'OAuth callback 処理中'
    window.location.replace(`${apiBase}/api/auth/callback${window.location.search}`)
  } else {
    refreshAuth()
  }
})
onBeforeUnmount(disconnect)
</script>

<template>
  <main class="shell">
    <header class="toolbar">
      <div>
        <h1>USER_VIEWSTATE_CHANGED</h1>
        <p>traQ WebSocket の通知を backend で受け取り、SSE で逐次表示します。</p>
      </div>
      <div class="actions">
        <span class="badge" :class="{ on: connected }">{{ status }}</span>
        <button v-if="!authenticated" type="button" @click="login">traQ OAuth</button>
        <button v-else type="button" @click="logout">ログアウト</button>
      </div>
    </header>

    <section class="stream">
      <div class="streamHeader">
        <strong>{{ events.length }} events</strong>
        <button type="button" :disabled="events.length === 0" @click="clearEvents">クリア</button>
      </div>

      <div v-if="events.length === 0" class="empty">
        USER_VIEWSTATE_CHANGED を受信するとここに JSON が追加されます。
      </div>

      <article v-for="item in newestFirst" :key="item.id" class="eventItem">
        <time>{{ item.receivedAt }}</time>
        <pre>{{ JSON.stringify(item.payload, null, 2) }}</pre>
      </article>
    </section>
  </main>
</template>
