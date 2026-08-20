<script setup>
// API 接口质量检测面板：站点详情内嵌
// 状态机：idle → queued/running → completed/failed/cancelled
// 创建后打开带认证 SSE；断开后退化为 1s polling；terminal 后加载最近历史。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { api } from '../api'
import { toast } from '../store'
import { mergeQualityEvent, isTerminalStatus, qualityLabel, normalizeStages } from '../quality'
import QualityStageTimeline from './QualityStageTimeline.vue'
import Icon from './Icon.vue'

const props = defineProps({
  channel: { type: Object, required: true },
  probeModelFallback: { type: String, default: '' },
})

const state = ref('idle') // idle | loading | queued | running | cancel_requested | completed | failed | cancelled
const runId = ref(null)
const selectedModel = ref('')
const runState = ref(null) // { status, overall_status, progress, current_stage, stages, error }（字段与后端 API 快照一致，snake_case）
const history = ref([])
const elapsed = ref(0)
let timer = null
let streamCtrl = null
let pollTimer = null
let abortCtrl = null

const reducedMotion = typeof window !== 'undefined'
  && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches

// 可选模型：test_model 优先 + 已映射模型
const modelOptions = computed(() => {
  const mapping = props.channel?.model_mapping || {}
  const keys = Object.keys(mapping)
  const opts = []
  const test = props.channel?.test_model
  if (test && keys.includes(test)) opts.push(test)
  for (const k of keys) {
    if (!opts.includes(k)) opts.push(k)
  }
  if (!opts.length && props.probeModelFallback) opts.push(props.probeModelFallback)
  return opts
})

const canRun = computed(() => modelOptions.value.length > 0 && !['queued', 'running', 'cancel_requested'].includes(state.value))

function resetPanel() {
  state.value = 'idle'
  runId.value = null
  runState.value = null
  history.value = []
  elapsed.value = 0
  stopAll()
}

function stopAll() {
  if (timer) { clearInterval(timer); timer = null }
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
  if (streamCtrl) { streamCtrl.stop(); streamCtrl = null }
  if (abortCtrl) { abortCtrl.abort(); abortCtrl = null }
}

// 页面刷新恢复：读取当前活跃任务。
// 捕获请求发起时的 channel id：快速切换站点时旧响应不得覆盖新站点面板。
async function restoreActive() {
  const cid = props.channel?.id
  try {
    const r = await api.listQualityChecks(cid, 1)
    if (props.channel?.id !== cid) return false // 已切换站点，丢弃过期响应
    const active = (r.runs || []).find(x => !isTerminalStatus(x.status))
    if (active) {
      runId.value = active.run_id
      runState.value = { ...active, stages: normalizeStages(active.stages) }
      state.value = active.status === 'cancel_requested' ? 'cancel_requested' : 'running'
      openStream()
      return true
    }
  } catch { /* 静默 */ }
  return false
}

async function loadHistory() {
  const cid = props.channel?.id
  try {
    const r = await api.listQualityChecks(cid, 5)
    if (props.channel?.id !== cid) return // 已切换站点，丢弃过期响应
    history.value = r.runs || []
  } catch { /* 已提示 */ }
}

async function startCheck() {
  if (!selectedModel.value) {
    const first = modelOptions.value[0]
    if (!first) { toast('该站点没有已映射模型，请先配置模型映射', 'error'); return }
    selectedModel.value = first
  }
  state.value = 'loading'
  try {
    const r = await api.createQualityCheck(props.channel.id, { model: selectedModel.value, depth: 'full' })
    runId.value = r.run_id
    runState.value = { ...r, stages: {} }
    state.value = 'queued'
    toast('质量检测任务已创建，可能产生少量上游费用', 'info', 4200)
    openStream()
  } catch (e) {
    if (e.status === 409) {
      // 同站点已有活跃任务：直接恢复它
      toast('该站点已有进行中的检测，已恢复进度', 'info')
      restoreActive()
    }
    state.value = 'idle'
  }
}

function openStream() {
  if (!runId.value) return
  stopAll()
  abortCtrl = new AbortController()
  startTimer()
  try {
    streamCtrl = api.streamQualityEvents(runId.value, {
      signal: abortCtrl.signal,
      onEvent: (ev) => handleEvent(ev),
      onDisconnect: () => fallbackPolling(),
    })
  } catch {
    fallbackPolling()
  }
}

function startTimer() {
  if (timer) clearInterval(timer)
  elapsed.value = 0
  timer = setInterval(() => { elapsed.value++ }, 1000)
}

function handleEvent(ev) {
  const merged = mergeQualityEvent(runState.value || {}, ev)
  runState.value = merged
  if (ev.event === 'task_started') {
    state.value = 'running'
  }
  if (ev.event === 'task_completed') {
    finish('completed', merged)
  } else if (ev.event === 'task_failed') {
    finish('failed', merged)
  } else if (ev.event === 'task_cancelled') {
    finish('cancelled', merged)
  }
}

function finish(status, merged) {
  state.value = status
  runState.value = merged
  stopAll()
  loadHistory()
}

// SSE 断开 → getQualityCheck 并退化为 1s polling
async function fallbackPolling() {
  if (pollTimer) return
  try {
    const r = await api.getQualityCheck(runId.value)
    runState.value = { ...r, stages: normalizeStages(r.stages) }
    if (isTerminalStatus(r.status)) {
      finish(r.status === 'completed' ? 'completed' : r.status, runState.value)
      return
    }
  } catch { /* 静默 */ }
  pollTimer = setInterval(async () => {
    try {
      const r = await api.getQualityCheck(runId.value)
      runState.value = { ...r, stages: normalizeStages(r.stages) }
      if (isTerminalStatus(r.status)) {
        finish(r.status === 'completed' ? 'completed' : r.status, runState.value)
      }
    } catch { /* 静默 */ }
  }, 1000)
}

async function cancelCheck() {
  if (!runId.value) return
  state.value = 'cancel_requested'
  try {
    await api.cancelQualityCheck(runId.value)
    toast('正在停止检测…', 'info')
  } catch { /* 已提示 */ }
}

function fmtDuration(s) {
  if (s < 60) return `${s} 秒`
  return `${Math.floor(s / 60)} 分 ${s % 60} 秒`
}

function fmtTime(iso) {
  if (!iso) return '—'
  try { return new Date(iso).toLocaleString('zh-CN', { hour12: false }) } catch { return iso }
}

function summaryOf(run) {
  const stages = run.stages || []
  const passed = stages.filter(s => s.status === 'passed').length
  const attention = stages.filter(s => s.status === 'attention').length
  const failed = stages.filter(s => s.status === 'failed').length
  return { passed, attention, failed }
}

// 历史项查看：加载完整任务详情（阶段结果；details 已由后端 allowlist 组装，
// 此处二次过滤防凭据形状字段）
function viewRun(run) {
  const cid = props.channel?.id
  runId.value = run.run_id
  api.getQualityCheck(run.run_id).then(detail => {
    if (props.channel?.id !== cid) return // 已切换站点，丢弃过期响应
    runState.value = { ...detail, stages: normalizeStages(detail.stages) }
    state.value = 'completed'
  }).catch(() => { /* 已提示 */ })
}

function copySummary(run) {
  const s = summaryOf(run)
  const text = `[Smart Router 质量检测] ${run.model} · ${qualityLabel(run.overall_status)}\n` +
    `通过 ${s.passed} · 关注 ${s.attention} · 失败 ${s.failed}\n时间: ${fmtTime(run.created_at)}`
  navigator.clipboard?.writeText(text).then(() => toast('摘要已复制', 'success'))
}

function rerun(run) {
  selectedModel.value = run.model
  startCheck()
}

watch(() => props.channel?.id, () => {
  resetPanel()
  selectedModel.value = ''
  restoreActive()
  loadHistory()
})

onMounted(() => {
  restoreActive().then(restored => { if (!restored) loadHistory() })
})
onUnmounted(() => stopAll())
</script>

<template>
  <div class="card card-pad qc-panel">
    <div class="row">
      <div class="set-title">API 接口质量检测</div>
      <span class="spacer" />
      <span v-if="runState?.overall_status" class="badge"
        :class="runState.overall_status === 'good' ? 'badge-green' : runState.overall_status === 'attention' ? 'badge-orange' : runState.overall_status === 'failed' ? 'badge-red' : 'badge-gray'">
        {{ qualityLabel(runState.overall_status) }}
      </span>
    </div>
    <p class="set-desc">复用已保存凭据，检测连接性/协议/流式/Usage/模型行为。full 深度最多发起两次小型聊天请求，<b>可能产生少量上游费用</b>；结果是启发式质量信号，不是绝对真实性证明。</p>

    <!-- 控制行 -->
    <div class="row gap-2 mb-3" style="flex-wrap:wrap;align-items:flex-end">
      <div class="field" style="margin-bottom:0">
        <label class="field-label">检测模型（临时，不修改站点配置）</label>
        <select v-model="selectedModel" class="input" style="min-width:190px" :disabled="!canRun">
          <option v-for="m in modelOptions" :key="m" :value="m">{{ m }}</option>
        </select>
      </div>
      <button v-if="canRun" class="btn btn-primary btn-sm" @click="startCheck" :disabled="state === 'loading'">
        <Icon name="bolt" :size="13" />{{ state === 'loading' ? '创建中…' : '一键质量检测' }}
      </button>
      <button v-if="['queued', 'running'].includes(state)" class="btn btn-ghost btn-sm" @click="cancelCheck">
        <Icon name="x" :size="12" />{{ state === 'cancel_requested' ? '正在停止…' : '停止' }}
      </button>
      <span v-if="['queued', 'running', 'cancel_requested'].includes(state)" class="text-3 mono" style="font-size:12px">
        已运行 {{ fmtDuration(elapsed) }}
      </span>
    </div>

    <!-- 无映射模型提示 -->
    <div v-if="!modelOptions.length" class="qc-hint">
      ⚠ 该站点没有可用的模型映射，请在「编辑站点 → 模型映射」中配置后再检测。
    </div>

    <!-- 阶段圆圈：状态/进度/hover 详情全部在圆圈中表达 -->
    <template v-if="runState">
      <QualityStageTimeline
        :stages="runState.stages || {}"
        :current-stage="runState.current_stage"
        :progress="runState.progress || 0"
        :depth="runState.depth || 'full'"
        :reduced-motion="reducedMotion"
      />
      <div v-if="runState.error" class="qc-error">⚠ {{ runState.error }}</div>
      <div v-if="runState" class="qc-tip-hint">悬停各阶段圆圈查看具体信息</div>
    </template>

    <!-- 历史 -->
    <div v-if="history.length" class="qc-history">
      <div class="field-label mb-1">最近 {{ history.length }} 次检测</div>
      <div v-for="run in history" :key="run.run_id" class="qc-history-row">
        <span class="badge" :class="run.overall_status === 'good' ? 'badge-green' : run.overall_status === 'attention' ? 'badge-orange' : run.overall_status === 'failed' ? 'badge-red' : 'badge-gray'">
          {{ qualityLabel(run.overall_status) }}
        </span>
        <span class="badge badge-blue mono">{{ run.model }}</span>
        <span class="text-3" style="font-size:12px">{{ fmtTime(run.created_at) }}</span>
        <span class="text-3" style="font-size:12px">
          ✓{{ summaryOf(run).passed }} ⚠{{ summaryOf(run).attention }} ✗{{ summaryOf(run).failed }}
        </span>
        <span class="row gap-1" style="margin-left:auto">
          <button class="btn btn-ghost btn-sm" @click="viewRun(run)">
            <Icon name="eye" :size="12" />查看
          </button>
          <button class="btn btn-ghost btn-sm" @click="copySummary(run)"><Icon name="copy" :size="12" /></button>
          <button class="btn btn-ghost btn-sm" @click="rerun(run)"><Icon name="refresh" :size="12" /></button>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.qc-panel { margin-bottom: 16px; }
.qc-hint {
  padding: 10px 14px; border-radius: var(--radius-md);
  background: var(--orange-soft); color: var(--orange); font-size: 12.5px; margin-bottom: 12px;
}
.qc-error {
  padding: 8px 12px; border-radius: var(--radius-md);
  background: var(--red-soft); color: var(--red); font-size: 12px; margin-bottom: 10px;
}
.qc-tip-hint { font-size: 11px; color: var(--text-3); margin-top: 4px; }
.qc-history { border-top: 1px solid var(--border); padding-top: 12px; }
.qc-history-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 8px 0; border-bottom: 1px solid var(--border); }
.qc-history-row:last-child { border-bottom: none; }
</style>
