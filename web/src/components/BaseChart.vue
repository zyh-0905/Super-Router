<script setup>
// ECharts 封装：props 传 option，自动初始化/更新/缩放/主题适配
import { ref, onMounted, onBeforeUnmount, watch, shallowRef } from 'vue'
import * as echarts from 'echarts'
import { resolvedTheme } from '../store'

const props = defineProps({
  option: { type: Object, required: true },
  height: { type: String, default: '240px' },
  // 可选：ECharts 事件名 → 处理函数（挂载时注册）
  events: { type: Object, default: () => ({}) },
})

const el = ref(null)
const chart = shallowRef(null)
let resizeObserver = null

// 主题相关的通用颜色，在 option 外合并
const themePalette = () => {
  const dark = resolvedTheme.value === 'dark'
  return {
    text: dark ? '#c7c7cc' : '#49494f',
    faint: dark ? '#8e8e93' : '#86868b',
    grid: dark ? 'rgba(255,255,255,0.10)' : 'rgba(0,0,0,0.08)',
    axisLabel: dark ? '#8e8e93' : '#86868b',
    splitLine: dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)',
  }
}

function render() {
  if (!chart.value || !props.option) return
  const t = themePalette()
  // 注入默认文字与轴线配色（组件内部 option 可覆盖）
  const opt = JSON.parse(JSON.stringify(props.option))
  if (opt.xAxis) {
    for (const ax of (Array.isArray(opt.xAxis) ? opt.xAxis : [opt.xAxis])) {
      ax.axisLine = ax.axisLine || { lineStyle: { color: t.grid } }
      ax.axisTick = ax.axisTick || { show: false }
      ax.axisLabel = ax.axisLabel || {}
      ax.axisLabel.color = ax.axisLabel.color || t.axisLabel
    }
  }
  if (opt.yAxis) {
    for (const ax of (Array.isArray(opt.yAxis) ? opt.yAxis : [opt.yAxis])) {
      ax.axisLine = ax.axisLine || { show: false }
      ax.axisTick = ax.axisTick || { show: false }
      ax.splitLine = ax.splitLine || { lineStyle: { color: t.splitLine } }
      ax.axisLabel = ax.axisLabel || {}
      ax.axisLabel.color = ax.axisLabel.color || t.axisLabel
    }
  }
  chart.value.setOption(opt, { notMerge: true })
}

onMounted(() => {
  chart.value = echarts.init(el.value, null, { renderer: 'canvas' })
  for (const [ev, handler] of Object.entries(props.events)) {
    if (typeof handler === 'function') chart.value.on(ev, handler)
  }
  render()
  resizeObserver = new ResizeObserver(() => chart.value && chart.value.resize())
  resizeObserver.observe(el.value)
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  chart.value?.dispose()
})

watch(() => props.option, render, { deep: true })
watch(resolvedTheme, () => setTimeout(render, 50))
</script>

<template>
  <div ref="el" :style="{ height, width: '100%' }" />
</template>
