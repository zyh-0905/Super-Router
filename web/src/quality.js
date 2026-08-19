// ============================================================
// 质量检测前端状态辅助（纯函数，无 Vue 依赖，便于 Node 测试）
//   - parseSSEChunk：SSE 帧增量解析（保留不完整尾块）
//   - mergeQualityEvent：SSE 事件 → 页面状态合并
//   - sanitizeQualityDetails：结果 details 凭据字段过滤
// ============================================================

const CREDENTIAL_KEYS = new Set([
  'authorization', 'api_key', 'access_token', 'balance_token',
  'bot_token', 'token', 'cookie', 'set-cookie', 'password',
])

// 敏感容器：整体丢弃（不保留容器内任何键）
const SENSITIVE_CONTAINERS = new Set(['headers', 'cookies', 'auth', 'credentials'])

/**
 * 解析 SSE 增量帧。返回 { events: [{event, data}], buffer }。
 * 帧以空行分隔；不完整尾块保留在 buffer 供下次拼接。
 * 支持 event/data 多行，不对非 JSON 行抛出未捕获异常。
 */
export function parseSSEChunk(buffer, chunk) {
  const text = buffer + chunk
  const frames = text.split(/\r?\n\r?\n/)
  // 最后一块可能是不完整帧（无空行结尾），保留
  const remainder = frames.pop() ?? ''

  const events = []
  for (const frame of frames) {
    let event = 'message'
    const dataLines = []
    for (let rawLine of frame.split('\n')) {
      if (rawLine.endsWith('\r')) rawLine = rawLine.slice(0, -1)
      if (rawLine.startsWith('event:')) {
        event = rawLine.slice(6).trim()
      } else if (rawLine.startsWith('data:')) {
        dataLines.push(rawLine.slice(5).replace(/^ /, ''))
      }
    }
    if (dataLines.length === 0) continue
    const payload = dataLines.join('\n')
    let data = null
    if (payload && payload !== '[DONE]') {
      try { data = JSON.parse(payload) } catch { data = payload }
    }
    events.push({ event, data })
  }
  return { events, buffer: remainder }
}

/**
 * 合并 SSE 事件到页面状态。阶段结果累积，进度/当前阶段更新。
 */
export function mergeQualityEvent(state, ev) {
  const s = { ...(state || {}), stages: { ...((state || {}).stages || {}) } }
  switch (ev.event) {
    case 'stage_started': {
      const stage = ev.data?.stage
      if (stage) s.stages[stage] = { ...(s.stages[stage] || {}), status: 'running' }
      if (ev.data?.progress != null) s.progress = ev.data.progress
      if (stage) s.currentStage = stage
      break
    }
    case 'stage_result': {
      const stage = ev.data?.stage
      if (stage) s.stages[stage] = { ...(s.stages[stage] || {}), ...ev.data }
      if (ev.data?.progress != null) s.progress = ev.data.progress
      break
    }
    case 'stage_progress':
    case 'task_progress': {
      if (ev.data?.progress != null) s.progress = ev.data.progress
      if (ev.data?.current_stage) s.currentStage = ev.data.current_stage
      if (ev.data?.status) s.status = ev.data.status
      break
    }
    case 'task_started':
    case 'task_completed':
    case 'task_failed':
    case 'task_cancelled': {
      if (ev.data) {
        if (ev.data.status) s.status = ev.data.status
        if (ev.data.progress != null) s.progress = ev.data.progress
        if (ev.data.overall_status) s.overallStatus = ev.data.overall_status
        if (ev.data.current_stage) s.currentStage = ev.data.current_stage
        // 完整快照事件直接合并全部阶段
        if (ev.data.stages) {
          for (const st of ev.data.stages) {
            s.stages[st.stage] = { ...(s.stages[st.stage] || {}), ...st }
          }
        }
      }
      break
    }
    default:
      // 未知事件忽略
  }
  return s
}

/**
 * 过滤 details 中的凭据形状字段（递归一层）。
 */
export function sanitizeQualityDetails(details) {
  if (details == null || typeof details !== 'object' || Array.isArray(details)) return details
  const out = {}
  for (const [k, v] of Object.entries(details)) {
    const key = String(k).toLowerCase()
    if (CREDENTIAL_KEYS.has(key) || /(secret|credential)/.test(key)) continue
    if (SENSITIVE_CONTAINERS.has(key)) continue // headers/cookies 等整体丢弃
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      // 递归过滤嵌套键（如 body.api_key）
      out[k] = sanitizeQualityDetails(v)
    } else {
      out[k] = v
    }
  }
  return out
}

const QUALITY_LABELS = {
  good: '良好',
  attention: '需要关注',
  failed: '异常',
  unknown: '无法判断',
}

const STAGE_LABELS = {
  connectivity: '连接性',
  protocol: '协议一致性',
  stream: '流式响应',
  usage: 'Usage/计费',
  behavior: '模型行为',
}

const TERMINAL = new Set(['completed', 'failed', 'cancelled', 'expired'])

export function qualityLabel(status) {
  return QUALITY_LABELS[status] || status || '—'
}

export function stageLabel(stage) {
  return STAGE_LABELS[stage] || stage
}

export function isTerminalStatus(status) {
  return TERMINAL.has(status)
}
