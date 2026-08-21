// ============================================================
// API 客户端 — 统一认证头、错误处理、连接状态维护
// ============================================================
import { store, toast } from './store'
import { telegramSubscriberTestPath } from './telegram'

function base() {
  return store.baseURL || '' // 空 = 同源（开发经 Vite 代理 / 生产由 Gateway 托管）
}

// F1：认证失效（401/403）统一处理。
// Key 失效后所有请求都会失败：清空 Key、标记认证失效状态、
// 提示用户去设置页重填。幂等：重复 401 只提示一次（防轮询刷屏）。
let authFailureNotified = false
export function handleAuthFailure() {
  if (authFailureNotified) return
  authFailureNotified = true
  store.apiKey = ''
  store.authExpired = true
  toast('API Key 已失效或无权访问，请在设置页重新填写', 'error', 6000)
}

// 重新填入有效 Key 后由 saveConnection 复位（见 store.js）。

export function authHeaders(extra = {}) {
  return { Authorization: `Bearer ${store.apiKey}`, ...extra }
}

async function request(path, { method = 'GET', body, headers = {}, timeout = 15000, silent = false } = {}) {
  const url = base() + path
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), timeout)
  try {
    const resp = await fetch(url, {
      method,
      headers: {
        'Content-Type': 'application/json',
        ...authHeaders(),
        ...headers,
      },
      body: body != null ? JSON.stringify(body) : undefined,
      signal: ctrl.signal,
    })
    clearTimeout(timer)
    let data = null
    const text = await resp.text()
    if (text) { try { data = JSON.parse(text) } catch { data = text } }
    if (!resp.ok) {
      const msg = (data && (data.error || data.message)) || `HTTP ${resp.status}`
      const err = new Error(msg)
      err.status = resp.status
      err.data = data
      // F1：认证失效处理——Key 被撤销/停用后，必须给出可操作的反馈，
      // 而不是让所有页面静默失败、轮询空转。
      if (resp.status === 401 || resp.status === 403) {
        handleAuthFailure()
      }
      throw err
    }
    store.connected = true
    return data
  } catch (e) {
    if (e.name === 'AbortError') {
      store.connected = false
      const err = new Error('请求超时，后端无响应')
      err.status = 0
      if (!silent) toast(err.message, 'error')
      throw err
    }
    if (e instanceof TypeError || e.message === 'Failed to fetch') {
      store.connected = false
      const err = new Error(`无法连接后端${base() ? ' (' + base() + ')' : ''}，请确认 Gateway 已启动`)
      err.status = 0
      if (!silent) toast(err.message, 'error')
      throw err
    }
    store.connected = true // 有 HTTP 响应说明服务可达
    if (!silent) toast(e.message, 'error')
    throw e
  }
}

export const api = {
  get: (path, opts) => request(path, { ...opts, method: 'GET' }),
  post: (path, body, opts) => request(path, { ...opts, method: 'POST', body }),
  put: (path, body, opts) => request(path, { ...opts, method: 'PUT', body }),
  patch: (path, body, opts) => request(path, { ...opts, method: 'PATCH', body }),
  del: (path, opts) => request(path, { ...opts, method: 'DELETE' }),

  // 健康检查（无认证）
  async ping() {
    const ctrl = new AbortController()
    const timer = setTimeout(() => ctrl.abort(), 4000)
    try {
      const resp = await fetch(base() + '/health', { signal: ctrl.signal })
      clearTimeout(timer)
      store.connected = resp.ok
      store.lastPingAt = new Date()
      return resp.ok
    } catch {
      clearTimeout(timer)
      store.connected = false
      return false
    }
  },

  // ===== 站点 =====
  listChannels: () => api.get('/admin/channels'),
  getChannel: (id) => api.get(`/admin/channels/${id}`),
  createChannel: (p) => api.post('/admin/channels', p),
  updateChannel: (id, p) => api.patch(`/admin/channels/${id}`, p),
  deleteChannel: (id) => api.del(`/admin/channels/${id}`),
  channelHealth: (id) => api.get(`/admin/health/${id}`),
  channelModels: (id) => api.get(`/admin/channels/${id}/models`),
  channelBalance: (id) => api.get(`/admin/channels/${id}/balance`),
  channelRatio: (id) => api.get(`/admin/channels/${id}/ratio`),
  channelMetrics: (groupId) => api.get('/admin/channel-metrics' + (groupId ? `?group_id=${groupId}` : '')),
  probeRatio: (id, model, maxTokens, official) => {
    const body = { model, max_tokens: maxTokens }
    if (official) {
      body.input_price_per_m = official.input
      body.output_price_per_m = official.output
    }
    return api.post(`/admin/channels/${id}/probe-ratio`, body, { timeout: 120000 })
  },
  listModelPrices: () => api.get('/admin/model-prices'),
  upsertModelPrice: (p) => api.post('/admin/model-prices', p),
  deleteModelPrice: (model) => api.del(`/admin/model-prices/${encodeURIComponent(model)}`),
  createRatioGroup: (id, p) => api.post(`/admin/channels/${id}/ratio-groups`, p),
  updateRatioGroup: (id, gid, p) => api.patch(`/admin/channels/${id}/ratio-groups/${gid}`, p),
  deleteRatioGroup: (id, gid) => api.del(`/admin/channels/${id}/ratio-groups/${gid}`),
  probeRatioGroup: (id, gid) => api.post(`/admin/channels/${id}/ratio-groups/${gid}/probe`, {}, { timeout: 120000 }),
  probeUpstreamModels: (base_url, api_key, protocol, access_token) => api.post('/admin/upstream/models', { base_url, api_key, protocol, access_token }, { timeout: 120000 }),
  getSettings: () => api.get('/admin/settings'),
  updateSettings: (p) => api.patch('/admin/settings', p),

  // ===== 分组 =====
  listGroups: () => api.get('/admin/groups'),
  createGroup: (p) => api.post('/admin/groups', p),
  updateGroup: (id, p) => api.patch(`/admin/groups/${id}`, p),
  deleteGroup: (id) => api.del(`/admin/groups/${id}`),

  // ===== 中转站归并 =====
  relayStations: () => api.get('/admin/relay-stations'),
  renameRelayStation: (id, display_name) => api.patch(`/admin/relay-stations/${id}`, { display_name }),

  // ===== 统计 / 决策 / 熔断（支持分组筛选） =====
  stats: (groupId) => api.get('/admin/stats' + (groupId ? `?group_id=${groupId}` : '')),
  alerts: (groupId) => api.get('/admin/alerts' + (groupId ? `?group_id=${groupId}` : '')),
  decisions: (limit = 50, groupId) => api.get(`/admin/decisions?limit=${limit}` + (groupId ? `&group_id=${groupId}` : '')),
  deleteDecisions: (requestIds) => api.del('/admin/decisions', { body: { request_ids: requestIds } }),
  circuit: (groupId) => api.get('/admin/circuit' + (groupId ? `?group_id=${groupId}` : '')),
  resetCircuit: (id) => api.post(`/admin/circuit/${id}/reset`, {}),

  // ===== API Keys =====
  listKeys: () => api.get('/admin/keys'),
  createKey: (role, groupIds = []) => api.post('/admin/keys', { role, group_ids: groupIds }),
  updateKey: (id, enabled, groupIds) => {
    const body = {}
    if (enabled != null) body.enabled = enabled
    if (groupIds) body.group_ids = groupIds
    return api.patch(`/admin/keys/${id}`, body)
  },
  deleteKey: (id) => api.del(`/admin/keys/${id}`),

  // ===== 运行配置 =====
  config: () => api.get('/admin/config'),

  // ===== Telegram 告警 =====
  getTelegramConfig: () => api.get('/admin/telegram/config'),
  updateTelegramConfig: (p) => api.patch('/admin/telegram/config', p),
  testTelegramConnection: (p) => api.post('/admin/telegram/test', p || {}),
  sendTelegramAlertSummary: () => api.post('/admin/telegram/report', {}),
  listTelegramSubscribers: () => api.get('/admin/telegram/subscribers'),
  createTelegramSubscriber: (p) => api.post('/admin/telegram/subscribers', p),
  updateTelegramSubscriber: (id, p) => api.patch(`/admin/telegram/subscribers/${id}`, p),
  deleteTelegramSubscriber: (id) => api.del(`/admin/telegram/subscribers/${id}`),
  sendTelegramSubscriberTest: (id) => api.post(telegramSubscriberTestPath(id), {}),
  getTelegramDeliveryLogs: () => api.get('/admin/telegram/delivery-logs'),

  // ===== API 接口质量检测 =====
  createQualityCheck: (channelId, payload) => api.post(`/admin/channels/${channelId}/quality-checks`, payload),
  listQualityChecks: (channelId, limit = 5) => api.get(`/admin/channels/${channelId}/quality-checks?limit=${limit}`),
  getQualityCheck: (runId) => api.get(`/admin/quality-checks/${runId}`),
  cancelQualityCheck: (runId) => api.post(`/admin/quality-checks/${runId}/cancel`, {}),

  /**
   * 带认证的 fetch SSE 流（原生 EventSource 无法携带 Bearer Header）。
   * 同步返回 { stop } 控制器（内部异步建立连接）；
   * 事件经 onEvent({event, data}) 回调，断开经 onDisconnect()。
   */
  streamQualityEvents(runId, { signal, onEvent, onDisconnect } = {}) {
    const ctrl = new AbortController()
    const abortFromSignal = () => ctrl.abort()
    if (signal) {
      if (signal.aborted) ctrl.abort()
      else signal.addEventListener('abort', abortFromSignal, { once: true })
    }

    const pump = async () => {
      let resp
      try {
        resp = await fetch(base() + `/admin/quality-checks/${runId}/events`, {
          headers: { ...authHeaders(), Accept: 'text/event-stream' },
          signal: ctrl.signal,
        })
      } catch (e) {
        if (!ctrl.signal.aborted) store.connected = false
        if (signal) signal.removeEventListener('abort', abortFromSignal)
        if (onDisconnect) onDisconnect()
        return
      }
      if (!resp.ok || !resp.body) {
        if (signal) signal.removeEventListener('abort', abortFromSignal)
        if (onDisconnect) onDisconnect()
        return
      }

      const reader = resp.body.getReader()
      const dec = new TextDecoder()
      const { parseSSEChunk } = await import('./quality')

      let buf = ''
      let gotTerminal = false
      const TERMINAL_EVENTS = ['task_completed', 'task_failed', 'task_cancelled']
      try {
        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          buf += dec.decode(value, { stream: true })
          const parsed = parseSSEChunk('', buf)
          buf = parsed.buffer
          for (const ev of parsed.events) {
            if (ev && TERMINAL_EVENTS.includes(ev.event)) gotTerminal = true
            if (onEvent) onEvent(ev)
          }
        }
        // 流结束：处理残留
        if (buf.trim()) {
          const parsed = parseSSEChunk('', buf + '\n\n')
          for (const ev of parsed.events) {
            if (ev && TERMINAL_EVENTS.includes(ev.event)) gotTerminal = true
            if (onEvent) onEvent(ev)
          }
        }
        // F3：服务端正常关闭（非异常）但未收到终态事件时，
        // 同样触发 onDisconnect 退化轮询——否则面板永久停在「运行中」。
        if (!gotTerminal && !ctrl.signal.aborted && onDisconnect) onDisconnect()
      } catch (e) {
        if (!ctrl.signal.aborted && onDisconnect) onDisconnect()
      } finally {
        if (signal) signal.removeEventListener('abort', abortFromSignal)
      }
    }
    pump() // 后台泵，不阻塞调用方
    return { stop: () => ctrl.abort() }
  },

  // ===== 策略中心 =====
  getPolicy: () => api.get('/admin/policies'),
  updatePolicy: (p) => api.put('/admin/policies', p),
  getGroupStrategy: (id) => api.get(`/admin/groups/${id}/strategy`),
  updateGroupStrategy: (id, p) => api.put(`/admin/groups/${id}/strategy`, p),

  // ===== 站点直达测试（测试台·站点测试页签；结果仅展示，不落库） =====
  // 探测耗时可能到 2 分钟（流式+非流式各一次），超时放宽到 300s
  siteTest: (channelId, payload) => api.post(`/admin/channels/${channelId}/site-test`, payload, { timeout: 300000 }),

  // ===== 推理代理（请求测试台用，返回原始 Response 以支持流式） =====
  async chatCompletion(payload, { onDelta } = {}) {
    const ctrl = new AbortController()
    const timer = setTimeout(() => ctrl.abort(), 300000)
    let resp
    try {
      resp = await fetch(base() + '/v1/chat/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...authHeaders() },
        body: JSON.stringify(payload),
        signal: ctrl.signal,
      })
    } catch (e) {
      clearTimeout(timer)
      store.connected = false
      throw new Error('无法连接后端，请确认 Gateway 已启动')
    }
    clearTimeout(timer)

    // 网关对可能含中文的响应头做了 URI 编码（HTTP 头按 Latin-1 解读会乱码），此处还原
    const safeDecode = (v) => {
      if (!v) return v
      try { return decodeURIComponent(v) } catch { return v }
    }
    const meta = {
      status: resp.status,
      channel: safeDecode(resp.headers.get('x-selected-channel')),
      channelId: resp.headers.get('x-selected-channel-id'),
      strategy: resp.headers.get('x-strategy'),
      group: safeDecode(resp.headers.get('x-group')),
      traceId: resp.headers.get('x-trace-id'),
      requestId: resp.headers.get('x-request-id'),
    }

    if (!resp.ok) {
      let errText = ''
      try { const d = await resp.json(); errText = d?.error?.message || d?.error || JSON.stringify(d) } catch { errText = await resp.text().catch(() => '') }
      const err = new Error(errText || `HTTP ${resp.status}`)
      err.status = resp.status
      err.meta = meta
      store.connected = true
      throw err
    }

    store.connected = true
    if (payload.stream && resp.body && onDelta) {
      const reader = resp.body.getReader()
      const dec = new TextDecoder()
      let buf = ''
      const parseLine = (line) => {
        const t = line.trim()
        if (!t.startsWith('data:')) return
        const data = t.slice(5).trim()
        if (!data || data === '[DONE]') return
        try {
          const obj = JSON.parse(data)
          const delta = obj.choices?.[0]?.delta?.content || obj.choices?.[0]?.text || ''
          if (delta) onDelta(delta)
        } catch { /* 非 JSON 行 */ }
      }
      while (true) {
        const { done, value } = await reader.read()
        if (done) break
        buf += dec.decode(value, { stream: true })
        const lines = buf.split('\n')
        buf = lines.pop()
        for (const line of lines) parseLine(line)
      }
      // 处理流结束前残留的最后一个未换行片段（尾块丢失修复）
      if (buf.trim()) {
        for (const line of buf.split('\n')) parseLine(line)
      }
      return { stream: true, meta, text: '' }
    }

    const data = await resp.json()
    return { stream: false, meta, data }
  },
}
