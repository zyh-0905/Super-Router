<script setup>
// 质量检测阶段圆圈：状态用颜色与图标表达，进度直接画在圆圈上。
//   waiting  = 灰空环
//   running  = 蓝色旋转弧（等待动画）+ 环内显示全局进度百分比
//   passed   = 绿色满环 + 勾
//   attention= 橙色满环 + 感叹号
//   failed   = 红色满环 + 叉
//   skipped  = 灰环 + 短横线
//   unknown  = 灰色虚线环 + 问号
// hover 任意非等待阶段显示详情气泡（状态/错误/指标），不再需要下方的进度条与描述行。
import { computed } from 'vue'
import Icon from './Icon.vue'
// F2：details 展示前必须过凭据字段过滤（纵深防御——
// 后端 allowlist 之外，前端再拦一道，防止凭据形状字段进入气泡）。
import { sanitizeQualityDetails } from '../quality'

const props = defineProps({
  stages: { type: Object, default: () => ({}) }, // stage -> {status, error, ...}
  currentStage: { type: String, default: '' },
  progress: { type: Number, default: 0 },
  depth: { type: String, default: 'full' },
  reducedMotion: { type: Boolean, default: false },
})

const ALL_STAGES = ['connectivity', 'protocol', 'stream', 'usage', 'behavior', 'authenticity']
const LABELS = {
  connectivity: '连接性',
  protocol: '协议一致性',
  stream: '流式响应',
  usage: 'Usage/计费',
  behavior: '模型行为',
  authenticity: '模型鉴定',
}
const STATUS_LABELS = {
  waiting: '待检测',
  running: '检测中',
  passed: '通过',
  attention: '需要关注',
  failed: '失败',
  skipped: '已跳过',
  unknown: '无法判断',
}

// 气泡中 details 常用键的中文映射（未映射的键原样显示）
const DETAIL_LABELS = {
  code: '代码',
  model_count: '模型数',
  events_received: 'SSE 事件',
  done_received: '[DONE] 标记',
  text_length: '输出文本',
  usage_present: 'usage 字段',
  responded: '已响应',
  reason: '原因',
  requested_model: '请求模型',
  mapped_model: '映射模型',
  expected_total: '期望总量',
}

// basic 深度只显示前三个阶段
const stageNames = computed(() =>
  props.depth === 'basic' ? ALL_STAGES.slice(0, 3) : ALL_STAGES)

function statusOf(name) {
  const s = props.stages?.[name]
  if (s?.status) return s.status
  if (props.currentStage === name) return 'running'
  return 'waiting'
}

function stageData(name) {
  return props.stages?.[name] || {}
}

function statusIcon(status) {
  switch (status) {
    case 'passed': return { icon: 'check' }
    case 'attention': return { icon: 'alert' }
    case 'failed': return { icon: 'x' }
    case 'skipped': return { text: '–' }
    case 'unknown': return { text: '?' }
    default: return {}
  }
}

// 气泡内容行（状态/错误/指标/详情），仅非等待阶段展示
function hasTooltip(name) {
  return statusOf(name) !== 'waiting'
}

function stageError(name) {
  return stageData(name).error || ''
}

function metricRows(name) {
  const s = stageData(name)
  const rows = []
  if (s.http_status) rows.push(`HTTP ${s.http_status}`)
  if (s.ttfb_ms != null) rows.push(`首字节 ${s.ttfb_ms}ms`)
  if (s.latency_ms != null) rows.push(`耗时 ${s.latency_ms}ms`)
  if (s.actual_model) rows.push(`上游模型 ${s.actual_model}`)
  if (s.prompt_tokens != null) rows.push(`tokens ${s.prompt_tokens}+${s.completion_tokens}`)
  const d = sanitizeQualityDetails(s.details) || {}
  for (const [k, v] of Object.entries(d)) {
    if (typeof v === 'object' || k === 'http_status') continue
    rows.push(`${DETAIL_LABELS[k] || k}: ${v}`)
  }
  return rows
}
</script>

<template>
  <div class="q-timeline" :class="{ 'no-anim': reducedMotion }">
    <div v-for="(name, i) in stageNames" :key="name" class="q-stage"
      :class="[statusOf(name), { current: currentStage === name }]">
      <div class="q-circle">
        <!-- 状态环：运行中为旋转弧，终态为满环，等待为空 -->
        <svg class="q-ring" viewBox="0 0 40 40" aria-hidden="true">
          <circle class="q-ring-track" cx="20" cy="20" r="17" />
          <circle v-if="statusOf(name) === 'running'" class="q-ring-spin" cx="20" cy="20" r="17" />
          <circle v-else-if="['passed', 'attention', 'failed'].includes(statusOf(name))"
            class="q-ring-fill" cx="20" cy="20" r="17" />
        </svg>
        <!-- 运行中：圆圈内显示全局进度百分比 -->
        <span v-if="statusOf(name) === 'running'" class="q-pct">
          {{ Math.max(0, Math.min(100, progress || 0)) }}%
        </span>
        <Icon v-else-if="statusIcon(statusOf(name)).icon" :name="statusIcon(statusOf(name)).icon" :size="15" />
        <span v-else-if="statusIcon(statusOf(name)).text" class="q-misc">{{ statusIcon(statusOf(name)).text }}</span>
      </div>
      <div class="q-label">{{ LABELS[name] }}</div>
      <div v-if="i < stageNames.length - 1" class="q-line" :class="'seg-' + statusOf(stageNames[i + 1])" />

      <!-- hover 详情气泡 -->
      <div v-if="hasTooltip(name)" class="q-tip" role="tooltip">
        <div class="q-tip-title">
          <span class="q-tip-status" :class="'tip-' + statusOf(name)">{{ STATUS_LABELS[statusOf(name)] }}</span>
          <span class="q-tip-stage">{{ LABELS[name] }}</span>
        </div>
        <div v-if="stageError(name)" class="q-tip-error">{{ stageError(name) }}</div>
        <div v-for="r in metricRows(name)" :key="r" class="q-tip-row">{{ r }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.q-timeline {
  position: relative;
  display: flex;
  padding: 16px 4px 10px; /* 顶部留出气泡空间 */
}
.q-stage {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  flex: 1 1 0;
  min-width: 0;
  text-align: center;
}
.q-stage:hover { z-index: 6; }

.q-circle {
  position: relative;
  width: 40px;
  height: 40px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--surface-solid);
  border: 1.5px solid var(--border-strong);
  color: var(--text-3);
  transition: all var(--dur) var(--ease);
  z-index: 1;
}

/* 状态环 SVG：稍大于圆圈本体，描边贴外沿 */
.q-ring {
  position: absolute;
  inset: -4px;
  width: 48px;
  height: 48px;
  pointer-events: none;
}
.q-ring-track { fill: none; stroke: var(--border); stroke-width: 2.5; }
.q-ring-fill { fill: none; stroke-width: 2.5; stroke-linecap: round; }
.q-ring-spin {
  fill: none;
  stroke: var(--blue);
  stroke-width: 2.5;
  stroke-linecap: round;
  stroke-dasharray: 28 79; /* 约 1/3 弧长 */
  transform-origin: 20px 20px;
  animation: q-rotate 1s linear infinite;
}
@keyframes q-rotate {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

.q-pct {
  font-family: var(--font-mono);
  font-size: 9.5px;
  font-weight: 600;
  color: var(--blue);
  line-height: 1;
}
.q-misc { font-size: 14px; font-weight: 700; }

.q-label {
  font-size: 11px;
  color: var(--text-3);
  margin-top: 8px;
  white-space: nowrap;
}

/* 连接线：颜色跟随下一阶段状态 */
.q-line {
  position: absolute;
  top: 24px; /* 圆圈中心线 */
  left: calc(50% + 27px);
  width: calc(100% - 54px);
  height: 2px;
  background: var(--border);
  border-radius: 1px;
  z-index: 0;
  transition: background var(--dur) var(--ease);
}
.q-line.seg-passed { background: var(--green); }
.q-line.seg-attention { background: var(--orange); }
.q-line.seg-failed { background: var(--red); }
.q-line.seg-running { background: var(--blue); }
.q-line.seg-skipped, .q-line.seg-unknown { background: var(--border-strong); }

/* ===== 状态配色 ===== */
.q-stage.passed .q-circle { border-color: var(--green); color: var(--green); background: var(--green-soft); }
.q-stage.passed .q-ring-fill { stroke: var(--green); }
.q-stage.passed .q-label { color: var(--green); }

.q-stage.attention .q-circle { border-color: var(--orange); color: var(--orange); background: var(--orange-soft); }
.q-stage.attention .q-ring-fill { stroke: var(--orange); }
.q-stage.attention .q-label { color: var(--orange); }

.q-stage.failed .q-circle { border-color: var(--red); color: var(--red); background: var(--red-soft); }
.q-stage.failed .q-ring-fill { stroke: var(--red); }
.q-stage.failed .q-label { color: var(--red); }

.q-stage.skipped .q-circle { border-color: var(--border-strong); color: var(--text-3); }
.q-stage.unknown .q-circle { border-style: dashed; }

/* 运行中：蓝色 + 呼吸光晕（等待动画与进度都在圆圈内） */
.q-stage.running .q-circle {
  border-color: var(--blue);
  background: var(--blue-soft);
  animation: q-breathe 1.6s ease-in-out infinite;
}
.q-stage.running .q-label { color: var(--blue); font-weight: 600; }
@keyframes q-breathe {
  0%, 100% { box-shadow: 0 0 0 0 rgba(0, 113, 227, 0.35); }
  50% { box-shadow: 0 0 0 7px rgba(0, 113, 227, 0); }
}
:root[data-theme="dark"] .q-stage.running .q-circle { animation-name: q-breathe-dark; }
@keyframes q-breathe-dark {
  0%, 100% { box-shadow: 0 0 0 0 rgba(10, 132, 255, 0.45); }
  50% { box-shadow: 0 0 0 7px rgba(10, 132, 255, 0); }
}

/* prefers-reduced-motion：禁用循环动画 */
.no-anim .q-stage.running .q-circle,
.no-anim .q-ring-spin { animation: none; }
@media (prefers-reduced-motion: reduce) {
  .q-stage.running .q-circle,
  .q-ring-spin { animation: none; }
}

/* ===== hover 详情气泡 ===== */
.q-tip {
  position: absolute;
  bottom: calc(100% - 4px); /* 相对整列（圆圈+标签）上方 */
  left: 50%;
  transform: translateX(-50%);
  width: max-content;
  max-width: 240px;
  padding: 8px 10px;
  background: var(--surface-raised);
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-raised);
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--text-1);
  opacity: 0;
  visibility: hidden;
  pointer-events: none;
  transition: opacity 0.15s var(--ease), visibility 0.15s;
  z-index: 10;
  text-align: left;
  white-space: nowrap;
}
.q-stage:hover .q-tip { opacity: 1; visibility: visible; }
/* 首尾阶段的气泡避免溢出容器 */
.q-stage:first-child .q-tip { left: 0; transform: none; }
.q-stage:last-child .q-tip { left: auto; right: 0; transform: none; }

.q-tip-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-weight: 600;
  margin-bottom: 2px;
}
.q-tip-status {
  font-size: 10px;
  font-weight: 600;
  padding: 1px 6px;
  border-radius: var(--radius-full);
}
.q-tip-status.tip-passed { background: var(--green-soft); color: var(--green); }
.q-tip-status.tip-attention { background: var(--orange-soft); color: var(--orange); }
.q-tip-status.tip-failed { background: var(--red-soft); color: var(--red); }
.q-tip-status.tip-running { background: var(--blue-soft); color: var(--blue); }
.q-tip-status.tip-skipped, .q-tip-status.tip-unknown { background: var(--border); color: var(--text-3); }

.q-tip-error {
  color: var(--red);
  font-family: var(--font-mono);
  font-size: 10.5px;
  margin: 2px 0;
  word-break: break-all;
}
.q-tip-row { color: var(--text-2); font-family: var(--font-mono); font-size: 10.5px; }
</style>
