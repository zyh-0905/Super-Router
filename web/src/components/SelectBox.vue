<script setup>
// 通用下拉选择器（Apple 风格）：统一替代原生 <select>。
// 特性：v-model 任意类型值、向上/向下自适应弹层、选中项对勾、
// 键盘导航（↑↓ Enter Esc）、点击外部/滚动自动跟随、入场弹跳动画。
import { ref, computed, nextTick, onMounted, onUnmounted } from 'vue'
import Icon from './Icon.vue'

const props = defineProps({
  modelValue: { type: [String, Number, Boolean, Object, null], default: null },
  options: { type: Array, default: () => [] }, // [{ value, label, disabled }]
  placeholder: { type: String, default: '请选择' },
  size: { type: String, default: 'md' }, // md | sm
  width: { type: String, default: '' },  // CSS 宽度（默认 100%）
  disabled: { type: Boolean, default: false },
})
const emit = defineEmits(['update:modelValue', 'change'])

const open = ref(false)
const triggerEl = ref(null)
const popEl = ref(null)
const pos = ref({ left: 0, top: 0, width: 0, up: false })
const activeIdx = ref(0)

const selected = computed(() => props.options.find(o => o.value === props.modelValue) || null)
const label = computed(() => (selected.value ? selected.value.label : props.placeholder))

function computePos() {
  const el = triggerEl.value
  if (!el) return
  const r = el.getBoundingClientRect()
  const estH = Math.min(props.options.length, 8) * 38 + 10
  const up = window.innerHeight - r.bottom < estH + 12 && r.top > estH + 12
  pos.value = { left: r.left, top: up ? r.top - 6 : r.bottom + 6, width: r.width, up }
}

function scrollActiveIntoView() {
  const item = popEl.value?.querySelector('.selbox-item.active')
  item?.scrollIntoView({ block: 'nearest' })
}

function openDropdown() {
  activeIdx.value = Math.max(0, props.options.findIndex(o => o.value === props.modelValue))
  computePos()
  open.value = true
  nextTick(() => {
    popEl.value?.focus()
    scrollActiveIntoView()
  })
}
function close() {
  open.value = false
  triggerEl.value?.focus()
}
function toggle() {
  if (props.disabled) return
  if (open.value) close()
  else openDropdown()
}
function pick(o) {
  if (o.disabled) return
  emit('update:modelValue', o.value)
  emit('change', o.value)
  close()
}
function onKey(e) {
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    activeIdx.value = Math.min(activeIdx.value + 1, props.options.length - 1)
    scrollActiveIntoView()
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    activeIdx.value = Math.max(activeIdx.value - 1, 0)
    scrollActiveIntoView()
  } else if (e.key === 'Enter') {
    e.preventDefault()
    const o = props.options[activeIdx.value]
    if (o) pick(o)
  } else if (e.key === 'Escape') {
    e.preventDefault()
    close()
  } else if (e.key === 'Tab') {
    close()
  }
}

function onDocMousedown(ev) {
  if (open.value && triggerEl.value && popEl.value &&
      !triggerEl.value.contains(ev.target) && !popEl.value.contains(ev.target)) {
    close()
  }
}
function onScrollResize() {
  if (open.value) computePos()
}

onMounted(() => {
  document.addEventListener('mousedown', onDocMousedown)
  window.addEventListener('scroll', onScrollResize, true)
  window.addEventListener('resize', onScrollResize)
})
onUnmounted(() => {
  document.removeEventListener('mousedown', onDocMousedown)
  window.removeEventListener('scroll', onScrollResize, true)
  window.removeEventListener('resize', onScrollResize)
})
</script>

<template>
  <div class="selbox" :style="{ width: width || '100%' }">
    <button
      ref="triggerEl" type="button"
      class="selbox-trigger" :class="['sz-' + size, { open, disabled }]"
      :aria-expanded="open"
      @click="toggle"
    >
      <span class="selbox-label truncate" :class="{ dim: !selected }">{{ label }}</span>
      <Icon name="chevron_down" :size="size === 'sm' ? 12 : 14" class="selbox-chevron" />
    </button>

    <Teleport to="body">
      <Transition name="selpop">
        <div
          v-if="open" ref="popEl" tabindex="-1"
          class="selbox-pop" :class="{ up: pos.up }"
          :style="{ left: pos.left + 'px', top: pos.top + 'px', width: pos.width + 'px' }"
          @keydown="onKey"
        >
          <div class="selbox-list">
            <button
              v-for="(o, i) in options" :key="i" type="button"
              class="selbox-item" :class="{ on: o.value === modelValue, active: i === activeIdx, disabled: o.disabled }"
              @click="pick(o)" @mouseenter="activeIdx = i"
            >
              <span class="truncate">{{ o.label }}</span>
              <Icon v-if="o.value === modelValue" name="check" :size="13" class="selbox-check" />
            </button>
            <div v-if="!options.length" class="selbox-empty">无可用选项</div>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.selbox { display: inline-block; }

.selbox-trigger {
  display: flex; align-items: center; gap: 8px;
  width: 100%; padding: 9px 13px;
  background: var(--surface-solid);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-md);
  color: var(--text-1); font-family: inherit; font-size: 13.5px;
  outline: none; cursor: pointer; text-align: left;
  transition: border-color var(--dur) var(--ease), box-shadow var(--dur) var(--ease);
}
.selbox-trigger:hover { border-color: color-mix(in srgb, var(--border-strong) 60%, var(--blue)); }
.selbox-trigger:focus, .selbox-trigger.open {
  border-color: var(--blue);
  box-shadow: 0 0 0 3.5px var(--blue-soft);
}
.selbox-trigger.disabled { opacity: 0.55; cursor: not-allowed; }
.selbox-trigger.sz-sm { padding: 5px 10px; font-size: 12px; border-radius: var(--radius-sm, 8px); }

.selbox-label { flex: 1; min-width: 0; }
.selbox-label.dim { color: var(--text-3); }
.selbox-chevron { flex-shrink: 0; color: var(--text-3); transition: transform 0.22s var(--ease); }
.selbox-trigger.open .selbox-chevron { transform: rotate(180deg); }

.selbox-pop {
  position: fixed; z-index: 1200; outline: none;
  padding: 6px; transform: translateY(-6px);
  background: var(--surface-raised);
  backdrop-filter: saturate(180%) blur(28px);
  -webkit-backdrop-filter: saturate(180%) blur(28px);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-modal);
  transform-origin: top center;
}
.selbox-pop.up { transform: translateY(6px); transform-origin: bottom center; }
.selbox-list { max-height: 288px; overflow-y: auto; display: flex; flex-direction: column; gap: 2px; }

.selbox-item {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 12px;
  border: none; border-radius: var(--radius-md);
  background: transparent; font-family: inherit;
  font-size: 13px; color: var(--text-1);
  cursor: pointer; text-align: left; width: 100%;
  transition: background var(--dur) var(--ease);
}
.selbox-item:hover, .selbox-item.active { background: var(--surface-hover); }
.selbox-item.on { font-weight: 600; }
.selbox-item.disabled { opacity: 0.45; cursor: not-allowed; }
.selbox-check { flex-shrink: 0; color: var(--blue); }
.selbox-empty { padding: 14px; text-align: center; font-size: 12px; color: var(--text-3); }

.selpop-enter-active { animation: selpopIn 0.22s cubic-bezier(0.34, 1.3, 0.64, 1); }
.selpop-leave-active { transition: opacity 0.12s ease; }
.selpop-leave-to { opacity: 0; }
@keyframes selpopIn {
  from { opacity: 0; transform: translateY(-6px) scale(0.97); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
.selbox-pop.up.selpop-enter-active { animation-name: selpopInUp; }
@keyframes selpopInUp {
  from { opacity: 0; transform: translateY(6px) scale(0.97); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
</style>
