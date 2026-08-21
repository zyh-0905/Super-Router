// ============================================================
// 全局状态（响应式，不引入 Pinia，保持轻量）
// ============================================================
import { reactive, computed } from 'vue'

// 凭据存储位置按环境区分：开发用 localStorage（方便调试），生产用 sessionStorage（会话结束即清）
const storage = import.meta.env.DEV ? window.localStorage : window.sessionStorage

// 管理员 Key 不再回退到开发 Key；仅开发环境无已存 Key 时预填 'test-admin-key' 便于调试，生产构建默认为空字符串
const storedKey = storage.getItem('sr_apikey') || ''
const savedKey = storedKey || (import.meta.env.DEV ? 'test-admin-key' : '')
const savedBase = (storage.getItem('sr_baseurl') || '').replace(/\/+$/, '')
const savedTheme = localStorage.getItem('sr_theme') || 'auto' // 'auto' | 'light' | 'dark'

export const store = reactive({
  // 连接配置
  apiKey: savedKey,
  baseURL: savedBase,
  theme: savedTheme,

  // 连接状态
  connected: false,
  lastPingAt: null,

  // 认证失效标记（401/403 后置位；保存新 Key 后复位）
  authExpired: false,

  // 全局告警（供侧边栏徽标使用）
  alerts: [],

  // 告警弹窗队列（需人工确认；头部第一条由 AlertPopupHost 展示）
  alertPopups: [],

  // 全局数据缓存
  stats: null,      // GET /admin/stats
  channels: [],     // GET /admin/channels
  groups: [],       // GET /admin/groups
  epoch: null,

  // 全局分组筛选器（null = 全部），各页面共享
  currentGroup: null,

  // Telegram 告警状态缓存（只保存非敏感状态：是否启用/最近汇总，绝不保存 Bot Token）
  telegram: null,

  // Toast 队列
  toasts: [],
})

export const resolvedTheme = computed(() => {
  if (store.theme === 'auto') {
    return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  }
  return store.theme
})

export function applyTheme() {
  const t = resolvedTheme.value
  if (t === 'dark') document.documentElement.setAttribute('data-theme', 'dark')
  else document.documentElement.setAttribute('data-theme', 'light')
}

export function setTheme(mode) {
  store.theme = mode
  localStorage.setItem('sr_theme', mode)
  applyTheme()
}

export function cycleTheme() {
  // auto -> light -> dark -> auto
  const order = ['auto', 'light', 'dark']
  const next = order[(order.indexOf(store.theme) + 1) % order.length]
  setTheme(next)
}

export function saveConnection() {
  storage.setItem('sr_apikey', store.apiKey)
  storage.setItem('sr_baseurl', store.baseURL)
  if (store.apiKey) store.authExpired = false
}

let toastId = 0
export function toast(message, type = 'info', duration = 3200) {
  const id = ++toastId
  store.toasts.push({ id, message, type })
  setTimeout(() => {
    const i = store.toasts.findIndex(t => t.id === id)
    if (i >= 0) store.toasts.splice(i, 1)
  }, duration)
}

// ============================================================
// 告警弹窗（全局）
// 语义：
//   - 首次喂入的告警集合作为基线（不弹窗）；
//   - 之后「新出现」或「严重度升级」的告警入队弹窗；
//   - 告警消失后再次出现会重新弹（恢复后再告警）；
//   - 仅 critical 级别弹窗（warning 只在侧边栏徽标与告警页体现）。
// ============================================================
const SEV_RANK = { critical: 2, warning: 1, info: 0 }
const alertSeen = new Map() // 告警 id -> 当前严重度 rank
let popupSeq = 0

export function feedAlerts(alerts) {
  const list = Array.isArray(alerts) ? alerts : []
  store.alerts = list

  const current = new Map()
  for (const a of list) {
    if (!a || !a.id) continue
    current.set(a.id, a)
    const rank = SEV_RANK[a.sev] ?? 1
    if (rank < 2) continue // 非 critical 不弹窗
    const prev = alertSeen.get(a.id)
    if (prev === undefined || rank > prev) {
      store.alertPopups.push({
        popupId: ++popupSeq,
        id: a.id,
        name: a.name,
        channel: a.channel || '',
        sev: 'critical',
        ago: a.ago || '',
        ts: Date.now(),
      })
    }
  }
  // 已消失的告警移出基线：恢复后再次触发会重新弹窗
  for (const id of alertSeen.keys()) {
    if (!current.has(id)) alertSeen.delete(id)
  }
  for (const [id, a] of current) {
    alertSeen.set(id, SEV_RANK[a.sev] ?? 1)
  }
}

export function dismissAlertPopup(popupId) {
  const i = store.alertPopups.findIndex(p => p.popupId === popupId)
  if (i >= 0) store.alertPopups.splice(i, 1)
}

export function dismissAllAlertPopups() {
  store.alertPopups = []
}
