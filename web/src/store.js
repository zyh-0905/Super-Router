// ============================================================
// 全局状态（响应式，不引入 Pinia，保持轻量）
// ============================================================
import { reactive, computed } from 'vue'

const savedKey = localStorage.getItem('sr_apikey') || 'test-admin-key'
const savedBase = (localStorage.getItem('sr_baseurl') || '').replace(/\/+$/, '')
const savedTheme = localStorage.getItem('sr_theme') || 'auto' // 'auto' | 'light' | 'dark'
const savedDefaultModel = localStorage.getItem('sr_default_model') || '' // 测试台默认模型

export const store = reactive({
  // 连接配置
  apiKey: savedKey,
  baseURL: savedBase,
  theme: savedTheme,

  // 连接状态
  connected: false,
  lastPingAt: null,

  // 全局告警（供侧边栏徽标使用）
  alerts: [],

  // 全局数据缓存
  stats: null,      // GET /admin/stats
  channels: [],     // GET /admin/channels
  groups: [],       // GET /admin/groups
  epoch: null,

  // 全局分组筛选器（null = 全部），各页面共享
  currentGroup: null,

  // 测试台默认模型（用户自定义，来自上游模型列表）
  defaultModel: savedDefaultModel,

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
  localStorage.setItem('sr_apikey', store.apiKey)
  localStorage.setItem('sr_baseurl', store.baseURL)
}

export function setDefaultModel(model) {
  store.defaultModel = model || ''
  localStorage.setItem('sr_default_model', store.defaultModel)
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
