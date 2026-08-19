<script setup>
// 统计卡：大数字 + 标签 + 底部对比行
// A11y: clickable 时支持键盘 Enter/Space
defineProps({
  label: { type: String, default: '' },
  value: { type: [String, Number], default: '' },
  unit: { type: String, default: '' },
  icon: { type: String, default: '' },
  color: { type: String, default: 'var(--blue)' },
  footHtml: { type: Boolean, default: false },
  clickable: { type: Boolean, default: false },
})
import Icon from './Icon.vue'

const emit = defineEmits(['click'])

function onKeydown(e) {
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    emit('click', e)
  }
}
</script>

<template>
  <div
    class="card stat-card"
    :class="{ clickable }"
    :tabindex="clickable ? 0 : undefined"
    :role="clickable ? 'button' : undefined"
    :aria-label="clickable ? label + ': ' + value + (unit || '') : undefined"
    @click="clickable && emit('click')"
    @keydown="clickable && onKeydown($event)"
  >
    <div class="stat-label">
      <Icon v-if="icon" :name="icon" :size="15" :style="{ color }" aria-hidden="true" />
      <span>{{ label }}</span>
    </div>
    <div class="stat-value">
      {{ value }}<span v-if="unit" class="unit">{{ unit }}</span>
    </div>
    <div class="stat-foot">
      <slot name="foot" />
    </div>
  </div>
</template>

<style scoped>
.stat-card { padding: 20px 22px; transition: transform var(--dur) var(--ease), box-shadow var(--dur) var(--ease); }
.stat-card.clickable { cursor: pointer; }
.stat-card.clickable:hover { transform: translateY(-2px); box-shadow: var(--shadow-raised); }
.stat-card.clickable:focus-visible {
  outline: 2.5px solid var(--focus-ring, var(--blue));
  outline-offset: 2px;
}
.stat-label { font-size: 12px; color: var(--text-3); margin-bottom: 10px; display: flex; align-items: center; gap: 7px; }
.stat-value { font-size: 32px; font-weight: 700; letter-spacing: -0.03em; line-height: 1; }
.unit { font-size: 15px; font-weight: 500; color: var(--text-3); margin-left: 4px; letter-spacing: 0; }
.stat-foot { margin-top: 10px; font-size: 12px; color: var(--text-3); display: flex; align-items: center; gap: 6px; }
</style>
