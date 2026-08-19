<script setup>
defineProps({
  icon: { type: String, default: 'layers' },
  title: { type: String, default: '暂无数据' },
  desc: { type: String, default: '' },
})
import Icon from './Icon.vue'
</script>

<template>
  <div class="empty" role="status">
    <div class="empty-icon"><Icon :name="icon" :size="30" aria-hidden="true" /></div>
    <div class="empty-title">{{ title }}</div>
    <div v-if="desc" class="empty-desc">{{ desc }}</div>
    <div class="mt-2"><slot /></div>
  </div>
</template>

<style scoped>
.empty { text-align: center; padding: 56px 24px; color: var(--text-3); }
.empty-icon { margin-bottom: 12px; opacity: 0.55; }
.empty-title { font-size: 15px; font-weight: 600; color: var(--text-2); margin-bottom: 5px; }
.empty-desc { font-size: 13px; }
</style>
