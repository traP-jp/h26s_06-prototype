import { ref } from 'vue'

// traQ OAuth のログイン状態を管理します。
// API 呼び出しと UI 状態をここに閉じ込め、App.vue は状態を読むだけにします。
export function useTraqAuth(apiBase, onLogout) {
  const authenticated = ref(false)
  const oauthConfigured = ref(false)
  const authStatus = ref('起動中')

  async function refreshAuth() {
    try {
      const response = await fetch(`${apiBase}/api/me`, { credentials: 'include' })
      if (!response.ok) {
        authStatus.value = `/api/me ${response.status}`
        return
      }
      const body = await response.json()
      authenticated.value = body.authenticated
      oauthConfigured.value = body.oauthConfigured
      authStatus.value = authenticated.value ? '認証済み' : 'デモ待機'
    } catch (error) {
      authStatus.value = `backend 未接続: ${error.message}`
    }
  }

  function login() {
    window.location.href = `${apiBase}/api/auth/login`
  }

  async function logout() {
    onLogout?.()
    await fetch(`${apiBase}/api/auth/logout`, { method: 'POST', credentials: 'include' })
    authenticated.value = false
    authStatus.value = 'ログアウト済み'
  }

  return {
    authenticated,
    oauthConfigured,
    authStatus,
    refreshAuth,
    login,
    logout,
  }
}
