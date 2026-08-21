// ============================================================
// 通用工具函数
// ============================================================

export function fmtTime(s) {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d)) return s
  return d.toLocaleTimeString('zh-CN', { hour12: false })
}

export function fmtDate(s) {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d)) return s
  return d.toLocaleString('zh-CN', { hour12: false, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

export function fmtAgo(s) {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d)) return s
  const sec = Math.max(0, (Date.now() - d.getTime()) / 1000)
  if (sec < 60) return '刚刚'
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`
  return `${Math.floor(sec / 86400)} 天前`
}

export function fmtNum(n) {
  if (n == null || isNaN(n)) return '—'
  return Number(n).toLocaleString('zh-CN')
}

export function fmtMs(n) {
  if (n == null || isNaN(n)) return '—'
  return `${Math.round(n)} ms`
}

export function fmtPct(n, digits = 1) {
  if (n == null || isNaN(n)) return '—'
  return `${(n * 100).toFixed(digits)}%`
}

export function fmtScore(s) {
  if (s == null || isNaN(s)) return '—'
  if (Math.abs(s) >= 100) return s.toFixed(0)
  if (Math.abs(s) >= 1) return s.toFixed(1)
  return s.toFixed(3)
}

export function scoreWidth(score, list) {
  const scores = (list || []).map(c => c.score).filter(s => s != null && !isNaN(s))
  if (!scores.length) return 0
  const max = Math.max(...scores)
  const min = Math.min(...scores)
  if (max === min) return 100
  return Math.max(4, Math.round(((score - min) / (max - min)) * 100))
}

export function maskKey(key) {
  if (!key) return '—'
  return key.length > 14 ? key.slice(0, 14) + '…' : key
}

export function downloadJSON(filename, obj) {
  const blob = new Blob([JSON.stringify(obj, null, 2)], { type: 'application/json' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  // 部分浏览器同步 revoke 会使下载竞态失败：延迟释放
  setTimeout(() => URL.revokeObjectURL(a.href), 0)
}
