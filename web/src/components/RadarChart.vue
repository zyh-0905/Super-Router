<script setup>
import { computed, ref } from 'vue'

// 手绘 SVG 六维曲线图：Catmull-Rom → 三次贝塞尔闭合曲线，渐变填充 + 光晕 + 悬停交互
const props = defineProps({
  // 候选列表：[{channel_id, channel, dims:{cost,reliability,...}}]
  details: { type: Array, default: () => [] },
  // 维度定义：[{key, label, color}]
  dims: { type: Array, required: true },
})
const emit = defineEmits(['hover'])

const palette = ['#0a84ff', '#30d158', '#ff9f0a', '#bf5af2', '#ff375f', '#64d2ff', '#ffd60a', '#ac8e68']

const SIZE = 360
const C = SIZE / 2
const R = 116
const LABEL_R = 140

const dimVal = (d, key) => Number(d?.dims?.[key] ?? 50)
const angle = i => (-90 + i * 60) * Math.PI / 180
const pt = (r, i) => [C + r * Math.cos(angle(i)), C + r * Math.sin(angle(i))]
const rFor = v => Math.max(8, Math.min(R, (v / 100) * R))

// 六点闭合 Catmull-Rom → 三次贝塞尔路径（精确过顶点、转角柔缓）
function curvePath(values) {
  const n = values.length
  const pts = values.map((v, i) => pt(rFor(v), i))
  let d = `M ${pts[0][0].toFixed(2)} ${pts[0][1].toFixed(2)}`
  for (let i = 0; i < n; i++) {
    const p0 = pts[(i - 1 + n) % n]
    const p1 = pts[i]
    const p2 = pts[(i + 1) % n]
    const p3 = pts[(i + 2) % n]
    const c1x = p1[0] + (p2[0] - p0[0]) / 6
    const c1y = p1[1] + (p2[1] - p0[1]) / 6
    const c2x = p2[0] - (p3[0] - p1[0]) / 6
    const c2y = p2[1] - (p3[1] - p1[1]) / 6
    d += ` C ${c1x.toFixed(2)} ${c1y.toFixed(2)}, ${c2x.toFixed(2)} ${c2y.toFixed(2)}, ${p2[0].toFixed(2)} ${p2[1].toFixed(2)}`
  }
  return d + ' Z'
}

const shapes = computed(() =>
  props.details.map((d, i) => ({
    ...d,
    color: palette[i % palette.length],
    path: curvePath(props.dims.map(dl => dimVal(d, dl.key))),
    points: props.dims.map(dl => pt(rFor(dimVal(d, dl.key)), props.dims.indexOf(dl))),
  }))
)

const hovered = ref(null)
const tip = ref({ show: false, x: 0, y: 0, idx: 0 })

function onEnter(i, ev) {
  hovered.value = i
  emit('hover', i)
  tip.value = { show: true, x: ev.offsetX, y: ev.offsetY, idx: i }
}
function onMove(ev) {
  if (tip.value.show) {
    tip.value.x = ev.offsetX
    tip.value.y = ev.offsetY
  }
}
function onLeave() {
  hovered.value = null
  emit('hover', null)
  tip.value.show = false
}

const shapeStyle = i => {
  const s = shapes.value[i]
  const dimmed = hovered.value != null && hovered.value !== i
  return {
    stroke: s.color,
    strokeWidth: hovered.value === i ? 3 : i === 0 ? 2.4 : 1.3,
    strokeOpacity: dimmed ? 0.22 : i === 0 ? 1 : 0.7,
    fillOpacity: dimmed ? 0.14 : i === 0 ? 0.9 : 0.5,
    filter: hovered.value === i || (hovered.value == null && i === 0) ? 'url(#radarGlow)' : 'none',
  }
}

const tipDetail = computed(() => shapes.value[tip.value.idx] || null)
</script>

<template>
  <div class="radar-wrap">
    <!-- 图例 -->
    <div class="radar-legend">
      <button
        v-for="(s, i) in shapes" :key="s.channel_id" type="button"
        class="legend-item" :class="{ on: hovered === i }"
        @mouseenter="hovered = i; emit('hover', i)" @mouseleave="onLeave"
      >
        <span class="lg-dot" :style="{ background: s.color, boxShadow: i === 0 ? '0 0 8px ' + s.color : 'none' }" />
        <span class="lg-name">{{ s.channel || ('#' + s.channel_id) }}</span>
        <span v-if="i === 0" class="lg-sel">已选</span>
      </button>
    </div>

    <!-- 图 -->
    <div class="radar-svg" @mousemove="onMove" @mouseleave="onLeave">
      <svg :viewBox="`0 0 ${SIZE} ${SIZE}`">
        <defs>
          <filter id="radarGlow" x="-50%" y="-50%" width="200%" height="200%">
            <feGaussianBlur stdDeviation="3.2" result="blur" />
            <feMerge><feMergeNode in="blur" /><feMergeNode in="SourceGraphic" /></feMerge>
          </filter>
          <radialGradient v-for="(s, i) in shapes" :key="'g' + s.channel_id" :id="`grad-${i}`">
            <stop offset="0%" :stop-color="s.color" :stop-opacity="i === 0 ? 0.28 : 0.16" />
            <stop offset="70%" :stop-color="s.color" :stop-opacity="0.08" />
            <stop offset="100%" :stop-color="s.color" :stop-opacity="0.02" />
          </radialGradient>
        </defs>

        <!-- 同心虚线环 -->
        <g v-for="r in [0.25, 0.5, 0.75, 1]" :key="'r' + r">
          <circle :cx="C" :cy="C" :r="R * r" fill="none" stroke="rgba(128,128,128,0.16)" stroke-dasharray="2 5" stroke-width="1" />
        </g>

        <!-- 轴线 + 维度标签 -->
        <g v-for="(dl, i) in dims" :key="'a' + dl.key">
          <line :x1="C" :y1="C" :x2="pt(R, i)[0]" :y2="pt(R, i)[1]" stroke="rgba(128,128,128,0.10)" stroke-width="1" />
          <circle :cx="pt(LABEL_R - 10, i)[0]" :cy="pt(LABEL_R - 10, i)[1]" :r="3" :fill="dl.color" :opacity="0.85" />
          <text :x="pt(LABEL_R, i)[0]" :y="pt(LABEL_R, i)[1] + 4" text-anchor="middle" class="axis-label">
            {{ dl.label }}
          </text>
        </g>

        <!-- 候选曲线（悬停热区：填充区域） -->
        <path
          v-for="(s, i) in shapes" :key="'s' + s.channel_id"
          :d="s.path"
          :fill="`url(#grad-${i})`"
          :stroke="shapeStyle(i).stroke"
          :stroke-width="shapeStyle(i).strokeWidth"
          :stroke-opacity="shapeStyle(i).strokeOpacity"
          :fill-opacity="shapeStyle(i).fillOpacity"
          :filter="shapeStyle(i).filter"
          class="radar-path"
          @mouseenter="onEnter(i, $event)" @mouseleave="onLeave"
        />

        <!-- 顶点圆点（悬停热区） -->
        <g v-for="(s, i) in shapes" :key="'p' + s.channel_id">
          <circle
            v-for="(p, j) in s.points" :key="j"
            :cx="p[0]" :cy="p[1]"
            :r="hovered === i ? 5 : i === 0 ? 3.8 : 2.8"
            :fill="s.color" stroke="#ffffff" stroke-width="1.4"
            style="transition: r 0.2s ease; cursor: pointer"
            @mouseenter="onEnter(i, $event)" @mouseleave="onLeave"
          />
        </g>
      </svg>

      <!-- 悬停提示卡 -->
      <Transition name="tip">
        <div v-if="tip.show && tipDetail" class="radar-tip" :style="{ left: tip.x + 'px', top: tip.y + 'px' }">
          <div class="tip-head">
            <span class="tip-dot" :style="{ background: tipDetail.color }" />
            <b>{{ tipDetail.channel || ('#' + tipDetail.channel_id) }}</b>
            <span v-if="tip.idx === 0" class="tip-sel">已选</span>
          </div>
          <div v-for="dl in dims" :key="dl.key" class="tip-row">
            <span class="tip-label" :style="{ color: dl.color }">{{ dl.label }}</span>
            <span class="tip-val">{{ dimVal(tipDetail, dl.key).toFixed(1) }}</span>
          </div>
        </div>
      </Transition>
    </div>
  </div>
</template>

<style scoped>
.radar-wrap { width: 100%; max-width: 470px; margin: 0 auto; }
.radar-legend {
  display: flex; flex-wrap: wrap; gap: 6px; justify-content: center; margin-bottom: 6px;
}
.legend-item {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 4px 10px; border-radius: 999px; border: 1px solid var(--border);
  background: var(--surface-solid); font-family: inherit;
  font-size: 11.5px; color: var(--text-2); cursor: pointer;
  transition: all var(--dur) var(--ease);
}
.legend-item:hover { color: var(--text-1); }
.legend-item.on { border-color: var(--blue); color: var(--text-1); background: var(--blue-soft); }
.lg-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; }
.lg-name { font-weight: 600; }
.lg-sel { font-size: 9.5px; color: #30d158; font-weight: 700; }

.radar-svg { position: relative; }
.radar-svg svg { display: block; }
.axis-label { font-size: 11px; font-weight: 600; fill: var(--text-3); }

.radar-path { transition: stroke-width 0.2s ease, stroke-opacity 0.2s ease, fill-opacity 0.2s ease, d 0.5s cubic-bezier(0.4, 0, 0.2, 1); }

.radar-tip {
  position: absolute; z-index: 30; pointer-events: none;
  transform: translate(12px, -50%);
  min-width: 150px; padding: 10px 12px;
  background: rgba(28, 28, 32, 0.94); border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 12px; box-shadow: 0 12px 32px rgba(0, 0, 0, 0.42);
  backdrop-filter: blur(10px); -webkit-backdrop-filter: blur(10px);
}
.tip-head { display: flex; align-items: center; gap: 6px; color: #f5f5f7; font-size: 12.5px; margin-bottom: 6px; }
.tip-dot { width: 8px; height: 8px; border-radius: 50%; }
.tip-sel { font-size: 10px; color: #30d158; font-weight: 700; }
.tip-row { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 1.5px 0; }
.tip-label { font-size: 11.5px; }
.tip-val { font-size: 12px; font-weight: 700; color: #f5f5f7; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }

.tip-enter-active, .tip-leave-active { transition: opacity 0.15s ease; }
.tip-enter-from, .tip-leave-to { opacity: 0; }
</style>
