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
  // 浅拷贝合并主题：不能用 JSON 克隆（会静默丢弃 tooltip.formatter 等函数）
  const opt = { ...props.option }
  const decorateAxis = ax => ({
    ...ax,
    axisLine: ax.axisLine || { lineStyle: { color: t.grid } },
    axisTick: ax.axisTick || { show: false },
    axisLabel: { ...(ax.axisLabel || {}), color: ax.axisLabel?.color || t.axisLabel },
  })
  if (opt.xAxis) {
    opt.xAxis = (Array.isArray(opt.xAxis) ? opt.xAxis : [opt.xAxis]).map(ax => decorateAxis(ax))
    if (!Array.isArray(props.option.xAxis)) opt.xAxis = opt.xAxis[0]
  }
  if (opt.yAxis) {
    opt.yAxis = (Array.isArray(opt.yAxis) ? opt.yAxis : [opt.yAxis]).map(ax => {
      const d = decorateAxis(ax)
      d.axisLine = d.axisLine || { show: false }
      d.axisTick = d.axisTick || { show: false }
      d.splitLine = ax.splitLine || { lineStyle: { color: t.splitLine } }
      return d
    })
    if (!Array.isArray(props.option.yAxis)) opt.yAxis = opt.yAxis[0]
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
// events 变化时重挂事件（先卸载旧事件避免重复触发）
watch(() => props.events, evts => {
  if (!chart.value) return
  chart.value.off()
  for (const [ev, handler] of Object.entries(evts || {})) {
    if (typeof handler === 'function') chart.value.on(ev, handler)
  }
}, { deep: true })
</script>

<template>
  <div ref="el" :style="{ height, width: '100%' }" />
</template>
