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
// 渲染签名：父组件每次重渲染都会新建 option 对象，签名相同则跳过 setOption，
// 避免悬停中的 tooltip 被无意义的重建闪掉。
let lastSig = ''

// 主题相关的通用颜色，在 option 外合并
const themePalette = () => {
  const dark = resolvedTheme.value === 'dark'
  return {
    text: dark ? '#c7c7cc' : '#49494f',
    faint: dark ? '#8e8e93' : '#86868b',
    grid: dark ? 'rgba(255,255,255,0.10)' : 'rgba(0,0,0,0.08)',
    axisLabel: dark ? '#8e8e93' : '#86868b',
    splitLine: dark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.05)',
    tooltipBg: dark ? 'rgba(44,44,46,0.94)' : 'rgba(255,255,255,0.97)',
  }
}

// tooltip 统一增强：挂到 body 逃逸 overflow 裁剪（迷你图/滚动容器内不再被切）、
// 限制在视口内、主题化外观（苹果风毛玻璃）。option 显式设置的值优先。
function decorateTooltip(tt, t) {
  if (!tt) return tt
  return {
    ...tt,
    appendToBody: tt.appendToBody != null ? tt.appendToBody : true,
    confine: tt.confine != null ? tt.confine : true,
    backgroundColor: tt.backgroundColor || t.tooltipBg,
    borderColor: tt.borderColor || t.grid,
    borderWidth: tt.borderWidth != null ? tt.borderWidth : 1,
    textStyle: { color: t.text, fontSize: 11, ...(tt.textStyle || {}) },
    extraCssText: (tt.extraCssText || '') + 'box-shadow:0 6px 20px rgba(0,0,0,0.15);backdrop-filter:blur(10px);',
  }
}

function optSig(opt) {
  return JSON.stringify(opt, (k, v) => (typeof v === 'function' ? String(v) : v))
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
  opt.tooltip = decorateTooltip(opt.tooltip, t)

  const sig = optSig(opt)
  if (sig === lastSig) return
  lastSig = sig
  chart.value.setOption(opt, { notMerge: true })
}

// 滚动任意祖先容器（如抽屉叠放）时收起 tooltip，避免其停留在错位位置
function onAnyScroll() {
  chart.value?.dispatchAction({ type: 'hideTip' })
}

onMounted(() => {
  chart.value = echarts.init(el.value, null, { renderer: 'canvas' })
  for (const [ev, handler] of Object.entries(props.events)) {
    if (typeof handler === 'function') chart.value.on(ev, handler)
  }
  render()
  resizeObserver = new ResizeObserver(() => chart.value && chart.value.resize())
  resizeObserver.observe(el.value)
  // capture 阶段监听：scroll 事件不冒泡，capture 可捕获任意嵌套滚动容器的滚动
  document.addEventListener('scroll', onAnyScroll, true)
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  document.removeEventListener('scroll', onAnyScroll, true)
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
