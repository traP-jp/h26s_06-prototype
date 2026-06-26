import { ref } from 'vue'
import { formatTime, shortUser, stateLabel } from '../utils/formatters.js'

// SSE の接続・切断と、受信したイベントを画面用状態へ反映する処理をまとめます。
export function useTraqEventStream(apiBase, sceneRef, activeChannels, hoveredChannel) {
  // EventSource が現在つながっているかを UI に見せるための状態です。
  const connected = ref(false)
  // HUD のステータスバッジに表示する短いメッセージです。
  const status = ref('起動中')
  // demo/live のどちらへ接続したかをボタン表示と接続 URL の生成に使います。
  const streamMode = ref('demo')
  // 最近受け取った msg/mov trigger の履歴です。
  const events = ref([])
  // viewer snapshot に含まれる総閲覧者数です。
  const viewerTotal = ref(0)
  // 閲覧者数が多いチャンネルの集計行です。
  const viewerChannels = ref([])
  // 最近状態が更新された viewer 行です。
  const viewerRecent = ref([])
  // snapshot の受信時刻を表示用文字列として持ちます。
  const viewerUpdatedAt = ref('')
  // viewer poll で今回サンプリングされたチャンネル数です。
  const sampledChannels = ref(0)
  // live モードで対象になっている全チャンネル数です。
  const totalChannels = ref(0)

  // ブラウザ標準の SSE 接続オブジェクトです。接続し直すため let で保持します。
  let source = null

  function connect(mode) {
    // mode を切り替える前に既存接続を閉じ、二重購読を防ぎます。
    disconnect()
    // UI の active 表示に使うため、要求された mode を先に保存します。
    streamMode.value = mode
    // EventSource が init を受け取るまでの暫定表示です。
    status.value = mode === 'demo' ? 'デモ接続中' : 'traQ 接続中'
    // demo のときだけ backend に ?demo=1 を付け、未認証でも動くストリームを要求します。
    const suffix = mode === 'demo' ? '?demo=1' : ''
    // withCredentials により、live mode で HttpOnly Cookie を backend へ送れます。
    source = new EventSource(`${apiBase}/api/events${suffix}`, { withCredentials: true })

    // init は最初に一度だけ届くチャンネルツリーで、Three.js のノード構築に使います。
    source.addEventListener('init', (event) => {
      // SSE の data は文字列なので JSON に戻します。
      const payload = JSON.parse(event.data)
      // sceneRef は Vue 側で作った Three.js シーンです。未生成時に備えて optional chaining にします。
      sceneRef.value?.setChannels(payload.channels)
      // init を受け取れたら画面を描ける状態なので接続済みにします。
      connected.value = true
      // mode に応じて、人が読めるステータスへ更新します。
      status.value = mode === 'demo' ? 'デモ受信中' : 'traQ 受信中'
    })

    // status は backend からの補助的な接続状態メッセージです。
    source.addEventListener('status', (event) => {
      // status が届く時点で SSE は開いているので connected を true にします。
      connected.value = true
      // backend が作った status 文字列をそのまま HUD に反映します。
      status.value = JSON.parse(event.data).status
    })

    // trigger は MESSAGE_CREATED や USER_VIEWSTATE_CHANGED から作られた可視化イベントです。
    source.addEventListener('trigger', (event) => {
      // payload.type が msg なら波紋、mov ならビームとして Three.js 側で扱います。
      const payload = JSON.parse(event.data)
      // 可視化シーンへイベントを渡し、ノードの熱量やエフェクトを更新します。
      sceneRef.value?.trigger(payload)
      // HUD の Events パネルにも短い履歴として残します。
      rememberEvent(payload)
    })

    // sync は backend 側で減衰した熱量をフロントへ同期するための差分イベントです。
    source.addEventListener('sync', (event) => {
      // deltas は channelId -> score の map です。
      const payload = JSON.parse(event.data)
      // Three.js 側の targetScore を backend の値へ近づけます。
      sceneRef.value?.sync(payload.deltas)
    })

    // viewers は live mode でだけ届く、チャンネル閲覧者のスナップショットです。
    source.addEventListener('viewers', (event) => {
      // backend が集計済みの viewer payload を JSON として受け取ります。
      const payload = JSON.parse(event.data)
      // 総数は metrics の Viewers に表示します。
      viewerTotal.value = payload.total ?? 0
      // チャンネル別集計は「閲覧中チャンネル」パネルに表示します。
      viewerChannels.value = payload.channels ?? []
      // 最近の閲覧行は「最近の閲覧」パネルに表示します。
      viewerRecent.value = payload.recent ?? []
      // サンプル数は live poll の粗さを UI で把握するために出します。
      sampledChannels.value = payload.sampledChannels ?? 0
      // 全チャンネル数も一緒に出し、sample x of y の y に使います。
      totalChannels.value = payload.totalChannels ?? 0
      // backend の Unix 秒をローカル時刻文字列へ変換します。
      viewerUpdatedAt.value = new Date((payload.ts ?? Date.now() / 1000) * 1000).toLocaleTimeString()
      // 開発中に viewer の詳細を DevTools で見やすくするため、表形式でも出します。
      console.table(
        viewerRecent.value.map((row) => ({
          user: shortUser(row.userId),
          channel: row.channelName,
          state: stateLabel(row.state),
          updatedAt: formatTime(row.updatedAt),
        })),
      )
    })

    // stream-error は backend が SSE として返したアプリケーションレベルのエラーです。
    source.addEventListener('stream-error', (event) => {
      // エラー時は接続済み表示を落とします。
      connected.value = false
      // data があれば JSON の error、なければ一般的な SSE エラーとして表示します。
      status.value = event.data ? JSON.parse(event.data).error : 'SSE エラー'
    })

    // onerror はネットワーク断や backend 停止など、EventSource 自体のエラーで呼ばれます。
    source.onerror = () => {
      // EventSource は自動再接続するため、UI は「待機中」としておきます。
      connected.value = false
      status.value = '再接続待機中'
    }
  }

  function disconnect() {
    // source が null なら既に切断済みなので何もしません。
    if (source) {
      // EventSource を閉じ、ブラウザ側の再接続も止めます。
      source.close()
      // 次回 connect 時に新しい EventSource を作れるよう参照を消します。
      source = null
    }
    // 手動切断後は UI の接続表示も落とします。
    connected.value = false
  }

  function markContextLost() {
    // WebGL context lost は描画を継続できない状態なので、ステータスに明示します。
    status.value = 'WebGL コンテキスト消失'
    // SSE が生きていても可視化は壊れているため、接続表示は off にします。
    connected.value = false
  }

  function rememberEvent(payload) {
    // msg は発生チャンネル、mov は移動先チャンネルをラベルにします。
    const label =
      payload.type === 'msg'
        ? `msg -> ${channelLabel(payload.ch)}`
        : `mov -> ${channelLabel(payload.to)}`
    // 新しいイベントを先頭へ入れ、現在時刻を人が読める形で残します。
    events.value.unshift({ id: makeId(), label, at: new Date().toLocaleTimeString() })
    // 表示対象は最大 16 件だけ保持し、メモリと UI の伸びを抑えます。
    events.value = events.value.slice(0, 16)
  }

  function channelLabel(id) {
    // Heat パネルに出ている activeChannels から名前が分かれば、それを優先します。
    const active = activeChannels.value.find((node) => node.id === id)
    if (active) return active.name
    // ホバー中のノードが対象なら、そこから名前を補完します。
    if (hoveredChannel.value?.id === id) return hoveredChannel.value.name
    // どちらにもない場合は、最低限 channel id をそのまま表示します。
    return id
  }

  function makeId() {
    // 対応ブラウザでは randomUUID を使い、なければ時刻と乱数で代替します。
    return crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`
  }

  // App.vue が使う状態と操作関数だけを返し、内部の EventSource は外へ出しません。
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
