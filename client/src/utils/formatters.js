// 画面表示用の短い文字列変換をまとめます。
// Vue コンポーネントから切り出すことで、SSE 処理やテンプレートを読みやすくします。
export function stateLabel(state) {
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

export function shortUser(userId) {
  if (!userId) return '-'
  return userId.length > 12 ? `${userId.slice(0, 8)}...` : userId
}

export function formatTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '-'
  return date.toLocaleTimeString()
}
