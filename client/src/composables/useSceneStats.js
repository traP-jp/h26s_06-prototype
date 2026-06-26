import { ref } from 'vue'

// Three.js シーンから届く統計値とホバー中ノードを Vue の状態として保持します。
export function useSceneStats() {
  // ChannelGraph に含まれるノード数です。
  const nodeCount = ref(0)
  // 現在表示中の ripple エフェクト数です。
  const rippleCount = ref(0)
  // 現在表示中の beam エフェクト数です。
  const beamCount = ref(0)
  // Heat パネルに出す、熱量上位のチャンネル一覧です。
  const activeChannels = ref([])
  // レイキャストで hover しているチャンネルです。
  const hoveredChannel = ref(null)

  function updateStats(stats) {
    // Three.js 側の emitStats が作った値を Vue の ref に反映します。
    nodeCount.value = stats.nodes
    rippleCount.value = stats.ripples
    beamCount.value = stats.beams
    activeChannels.value = stats.activeChannels
  }

  function updateHover(node) {
    // null が来た場合は focusPanel を非表示にします。
    hoveredChannel.value = node
  }

  // App.vue から読みたい ref と、Three.js callback に渡す更新関数を返します。
  return {
    nodeCount,
    rippleCount,
    beamCount,
    activeChannels,
    hoveredChannel,
    updateStats,
    updateHover,
  }
}
