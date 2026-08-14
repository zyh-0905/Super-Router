<script setup>
// 统计卡：大数字 + 标签 + 底部对比行
defineProps({
  label: String,
  value: [String, Number],
  unit: String,
  icon: String,
  color: { type: String, default: 'var(--blue)' },
  footHtml: { type: Boolean, default: false },
  clickable: { type: Boolean, default: false },
})
import Icon from './Icon.vue'
</script>

<template>
  <div class="card stat-card" :class="{ clickable }">
    <div class="stat-label">
      <Icon v-if="icon" :name="icon" :size="15" :style="{ color }" />
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
.stat-label { font-size: 12px; color: var(--text-3); margin-bottom: 10px; display: flex; align-items: center; gap: 7px; }
.stat-value { font-size: 32px; font-weight: 700; letter-spacing: -0.03em; line-height: 1; }
.unit { font-size: 15px; font-weight: 500; color: var(--text-3); margin-left: 4px; letter-spacing: 0; }
.stat-foot { margin-top: 10px; font-size: 12px; color: var(--text-3); display: flex; align-items: center; gap: 6px; }
</style>
