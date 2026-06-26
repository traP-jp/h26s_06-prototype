import { ref } from 'vue'
import { formatTime, shortUser, stateLabel } from '../utils/formatters.js'

// SSE の接続・切断と、受信したイベントを画面用状態へ反映する処理をまとめます。
export function useTraqEventStream(apiBase, sceneRef, activeChannels, hoveredChannel) {
  const connected = ref(false)
  const status = ref('起動中')
  const streamMode = ref('demo')
  const events = ref([])
  const viewerTotal = ref(0)
  const viewerChannels = ref([])
  const viewerRecent = ref([])
  const viewerUpdatedAt = ref('')
  const sampledChannels = ref(0)
  const totalChannels = ref(0)

  let source = null

  function connect(mode) {
    disconnect()
    streamMode.value = mode
    status.value = mode === 'demo' ? 'デモ接続中' : 'traQ 接続中'
    const suffix = mode === 'demo' ? '?demo=1' : ''
    source = new EventSource(`${apiBase}/api/events${suffix}`, { withCredentials: true })

    source.addEventListener('init', (event) => {
      const payload = JSON.parse(event.data)
      sceneRef.value?.setChannels(payload.channels)
      connected.value = true
      status.value = mode === 'demo' ? 'デモ受信中' : 'traQ 受信中'
    })

    source.addEventListener('status', (event) => {
      connected.value = true
      status.value = JSON.parse(event.data).status
    })

    source.addEventListener('trigger', (event) => {
      const payload = JSON.parse(event.data)
      sceneRef.value?.trigger(payload)
      rememberEvent(payload)
    })

    source.addEventListener('sync', (event) => {
      const payload = JSON.parse(event.data)
      sceneRef.value?.sync(payload.deltas)
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

  function markContextLost() {
    status.value = 'WebGL コンテキスト消失'
    connected.value = false
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

  function makeId() {
    return crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`
  }

  return {
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
  }
}
