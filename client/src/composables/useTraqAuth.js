import { ref } from 'vue'

// traQ OAuth のログイン状態を管理します。
// API 呼び出しと UI 状態をここに閉じ込め、App.vue は状態を読むだけにします。
export function useTraqAuth(apiBase, onLogout) {
  // backend がセッション Cookie を認識しているかどうかです。
  const authenticated = ref(false)
  // TRAQ_CLIENT_ID が backend に設定されているかどうかです。
  const oauthConfigured = ref(false)
  // 認証まわりの結果を HUD の status へ反映するための文字列です。
  const authStatus = ref('起動中')

  async function refreshAuth() {
    try {
      // Cookie を送るため credentials: 'include' を付けて /api/me を呼びます。
      const response = await fetch(`${apiBase}/api/me`, { credentials: 'include' })
      if (!response.ok) {
        // HTTP エラーはそのまま status に出し、backend 側の問題を見つけやすくします。
        authStatus.value = `/api/me ${response.status}`
        return
      }
      // backend は authenticated/oauthConfigured の bool だけを返します。
      const body = await response.json()
      // live ボタンの有効/無効に使う認証状態です。
      authenticated.value = body.authenticated
      // OAuth ボタンの有効/無効に使う設定状態です。
      oauthConfigured.value = body.oauthConfigured
      // 認証済みなら「認証済み」、未認証なら demo 待機中として表示します。
      authStatus.value = authenticated.value ? '認証済み' : 'デモ待機'
    } catch (error) {
      // backend 未起動やネットワーク不通はここで UI に表示します。
      authStatus.value = `backend 未接続: ${error.message}`
    }
  }

  function login() {
    // OAuth は backend 側で state を作るため、フロントは login endpoint へ遷移するだけです。
    window.location.href = `${apiBase}/api/auth/login`
  }

  async function logout() {
    // live/demo SSE が開いていれば、backend セッション破棄の前に閉じます。
    onLogout?.()
    // backend の session map と Cookie を削除します。
    await fetch(`${apiBase}/api/auth/logout`, { method: 'POST', credentials: 'include' })
    // UI 側も即座に未認証へ戻します。
    authenticated.value = false
    // App.vue がこの値を status に反映します。
    authStatus.value = 'ログアウト済み'
  }

  // App.vue が必要な状態と操作だけを返します。
  return {
    authenticated,
    oauthConfigured,
    authStatus,
    refreshAuth,
    login,
    logout,
  }
}
