import { ref } from 'vue'

// Three.js シーンから届く統計値とホバー中ノードを Vue の状態として保持します。
export function useSceneStats() {
  const nodeCount = ref(0)
  const rippleCount = ref(0)
  const beamCount = ref(0)
  const activeChannels = ref([])
  const hoveredChannel = ref(null)

  function updateStats(stats) {
    nodeCount.value = stats.nodes
    rippleCount.value = stats.ripples
    beamCount.value = stats.beams
    activeChannels.value = stats.activeChannels
  }

  function updateHover(node) {
    hoveredChannel.value = node
  }

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
