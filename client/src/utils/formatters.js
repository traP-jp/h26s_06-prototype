// 画面表示用の短い文字列変換をまとめます。
// Vue コンポーネントから切り出すことで、SSE 処理やテンプレートを読みやすくします。
export function stateLabel(state) {
  // traQ API の state を、日本語 UI 表示へ変換します。
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
      // 未知の state はデバッグしやすいよう、そのまま表示します。
      return state || '-'
  }
}

export function shortUser(userId) {
  // 空のユーザー ID はプレースホルダにします。
  if (!userId) return '-'
  // 長い UUID は HUD の幅を圧迫するため、先頭だけ表示します。
  return userId.length > 12 ? `${userId.slice(0, 8)}...` : userId
}

export function formatTime(value) {
  // 値がない場合は表示できる時刻がないので '-' を返します。
  if (!value) return '-'
  // backend の ISO 文字列/日時値を Date に変換します。
  const date = new Date(value)
  // パースできない値は Invalid Date を出さず '-' にします。
  if (Number.isNaN(date.getTime())) return '-'
  // ブラウザのロケール設定に合わせた時刻だけを表示します。
  return date.toLocaleTimeString()
}
