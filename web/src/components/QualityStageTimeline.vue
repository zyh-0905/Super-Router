<script setup>
// 质量检测阶段时间线：固定五阶段，各状态视觉表现
// waiting=灰点 / running=蓝色呼吸圆点 / passed=绿勾 / attention=橙感叹号 /
// failed=红叉 / skipped=灰短横线 / unknown=灰问号
import Icon from './Icon.vue'

defineProps({
  stages: { type: Object, default: () => ({}) }, // stage -> {status, ...}
  currentStage: { type: String, default: '' },
  progress: { type: Number, default: 0 },
  reducedMotion: { type: Boolean, default: false },
})

const FIXED_STAGES = ['connectivity', 'protocol', 'stream', 'usage', 'behavior']
const LABELS = {
  connectivity: '连接性',
  protocol: '协议一致性',
  stream: '流式响应',
  usage: 'Usage/计费',
  behavior: '模型行为',
}

function statusOf(stages, name, currentStage) {
  const s = stages?.[name]
  if (s?.status) return s.status
  if (currentStage === name) return 'running'
  return 'waiting'
}

function statusIcon(status) {
  switch (status) {
    case 'passed': return { icon: 'check', cls: 'ok' }
    case 'attention': return { icon: 'alert', cls: 'warn' }
    case 'failed': return { icon: 'x', cls: 'fail' }
    case 'running': return { icon: '', cls: 'running' }
    case 'skipped': return { icon: '', cls: 'skipped' }
    case 'unknown': return { icon: '', cls: 'unknown' }
    default: return { icon: '', cls: 'waiting' }
  }
}
</script>

<template>
  <div class="q-timeline" :class="{ 'no-anim': reducedMotion }">
    <div v-for="(name, i) in FIXED_STAGES" :key="name" class="q-stage"
      :class="[statusOf(stages, name, currentStage), { current: currentStage === name }]">
      <div class="q-dot">
        <template v-if="statusIcon(statusOf(stages, name, currentStage)).icon">
          <Icon :name="statusIcon(statusOf(stages, name, currentStage)).icon" :size="13" />
        </template>
        <template v-else-if="statusOf(stages, name, currentStage) === 'skipped'">
          <span class="q-dash">–</span>
        </template>
        <template v-else-if="statusOf(stages, name, currentStage) === 'unknown'">
          <span class="q-qm">?</span>
        </template>
      </div>
      <div class="q-label">{{ LABELS[name] }}</div>
      <div v-if="i < FIXED_STAGES.length - 1" class="q-line" />
    </div>
    <div class="q-progressbar">
      <div class="q-progress-fill" :style="{ width: progress + '%' }" />
    </div>
  </div>
</template>

<style scoped>
.q-timeline { position: relative; padding: 8px 4px 22px; }
.q-stage {
  display: inline-flex; flex-direction: column; align-items: center;
  position: relative; width: 20%; text-align: center;
}
.q-dot {
  width: 26px; height: 26px; border-radius: 50%;
  display: flex; align-items: center; justify-content: center;
  background: var(--surface-solid); border: 1.5px solid var(--border-strong);
  color: var(--text-3); font-size: 12px; font-weight: 700;
  transition: all var(--dur) var(--ease);
  z-index: 1;
}
.q-label { font-size: 11px; color: var(--text-3); margin-top: 6px; white-space: nowrap; }
.q-line {
  position: absolute; top: 13px; left: calc(50% + 14px); width: calc(100% - 28px);
  height: 2px; background: var(--border); z-index: 0;
}

/* 各状态 */
.q-stage.ok .q-dot { border-color: var(--green); color: var(--green); background: var(--green-soft); }
.q-stage.ok .q-label { color: var(--green); }
.q-stage.warn .q-dot { border-color: var(--orange); color: var(--orange); background: var(--orange-soft); }
.q-stage.warn .q-label { color: var(--orange); }
.q-stage.fail .q-dot { border-color: var(--red); color: var(--red); background: var(--red-soft); }
.q-stage.fail .q-label { color: var(--red); }
.q-stage.skipped .q-dot { border-color: var(--border-strong); color: var(--text-3); }
.q-stage.unknown .q-dot { border-style: dashed; }

/* 呼吸动画（running） */
.q-stage.running .q-dot {
  border-color: var(--blue); background: var(--blue-soft);
  animation: q-breathe 1.6s ease-in-out infinite;
}
.q-stage.running .q-label { color: var(--blue); font-weight: 600; }
@keyframes q-breathe {
  0%, 100% { box-shadow: 0 0 0 0 rgba(0, 113, 227, 0.35); }
  50% { box-shadow: 0 0 0 6px rgba(0, 113, 227, 0); }
}
:root[data-theme="dark"] .q-stage.running .q-dot { animation-name: q-breathe-dark; }
@keyframes q-breathe-dark {
  0%, 100% { box-shadow: 0 0 0 0 rgba(10, 132, 255, 0.45); }
  50% { box-shadow: 0 0 0 6px rgba(10, 132, 255, 0); }
}

/* prefers-reduced-motion：禁用循环动画 */
.no-anim .q-stage.running .q-dot { animation: none; }
@media (prefers-reduced-motion: reduce) {
  .q-stage.running .q-dot { animation: none; }
}

/* 进度条 */
.q-progressbar {
  position: absolute; left: 4px; right: 4px; bottom: 6px;
  height: 3px; border-radius: 2px; background: var(--border); overflow: hidden;
}
.q-progress-fill {
  height: 100%; background: var(--blue); border-radius: 2px;
  transition: width 0.4s var(--ease);
}
</style>
