<script setup>
// 通用弹窗：毛玻璃遮罩 + 居中卡片
// A11y: Escape 关闭、焦点陷阱、aria-modal
import { onMounted, onUnmounted, ref, nextTick } from 'vue'
import Icon from './Icon.vue'

defineProps({
  title: { type: String, default: '' },
  width: { type: String, default: '520px' },
  showClose: { type: Boolean, default: true },
})
const emit = defineEmits(['close'])

const sheetRef = ref(null)
const titleId = `modal-title-${Math.random().toString(36).slice(2, 9)}`
let previousActiveElement = null

// 焦点陷阱：收集弹窗内所有可聚焦元素
function getFocusableElements() {
  if (!sheetRef.value) return []
  return [...sheetRef.value.querySelectorAll(
    'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
  )].filter(el => !el.disabled && el.offsetParent !== null)
}

function onKeydown(e) {
  if (e.key === 'Escape') {
    e.stopPropagation()
    emit('close')
    return
  }
  if (e.key === 'Tab') {
    const focusable = getFocusableElements()
    if (!focusable.length) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (e.shiftKey) {
      if (document.activeElement === first || !sheetRef.value?.contains(document.activeElement)) {
        e.preventDefault()
        last.focus()
      }
    } else {
      if (document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }
  }
}

onMounted(() => {
  previousActiveElement = document.activeElement
  document.addEventListener('keydown', onKeydown, true)
  // 自动聚焦到弹窗
  nextTick(() => {
    const focusable = getFocusableElements()
    if (focusable.length) focusable[0].focus()
    else sheetRef.value?.focus()
  })
  // 防止背景滚动
  document.body.style.overflow = 'hidden'
})

onUnmounted(() => {
  document.removeEventListener('keydown', onKeydown, true)
  document.body.style.overflow = ''
  // 恢复焦点
  if (previousActiveElement && previousActiveElement.focus) {
    previousActiveElement.focus()
  }
})
</script>

<template>
  <Teleport to="body">
    <div class="modal-overlay" @mousedown.self="emit('close')">
      <div
        ref="sheetRef"
        class="modal-sheet"
        :style="{ width }"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="title ? titleId : undefined"
        tabindex="-1"
      >
        <div class="modal-head">
          <span class="modal-title" :id="titleId">{{ title }}</span>
          <button
            v-if="showClose"
            class="icon-btn"
            style="width:28px;height:28px"
            aria-label="关闭弹窗"
            @click="emit('close')"
          >
            <Icon name="x" :size="14" />
          </button>
        </div>
        <div class="modal-body">
          <slot />
        </div>
        <div v-if="$slots.footer" class="modal-foot">
          <slot name="footer" />
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.modal-overlay {
  position: fixed; inset: 0; z-index: var(--z-modal);
  background: rgba(0, 0, 0, 0.32);
  backdrop-filter: blur(8px);
  -webkit-backdrop-filter: blur(8px);
  display: flex; align-items: center; justify-content: center;
  padding: 24px;
  animation: overlayIn 0.22s ease;
}
@keyframes overlayIn { from { opacity: 0; } to { opacity: 1; } }

.modal-sheet {
  max-width: 94vw; max-height: 88vh;
  display: flex; flex-direction: column;
  background: var(--surface-raised);
  backdrop-filter: saturate(180%) blur(28px);
  -webkit-backdrop-filter: saturate(180%) blur(28px);
  border: 1px solid var(--border);
  border-radius: var(--radius-xl);
  box-shadow: var(--shadow-modal);
  animation: sheetIn 0.28s cubic-bezier(0.32, 0.72, 0, 1);
  outline: none;
}
@keyframes sheetIn { from { opacity: 0; transform: scale(0.96) translateY(10px); } to { opacity: 1; transform: none; } }

.modal-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 18px 22px; border-bottom: 1px solid var(--border); flex-shrink: 0;
}
.modal-title { font-size: 17px; font-weight: 700; letter-spacing: -0.01em; }
.modal-body { padding: 20px 22px; overflow-y: auto; }
.modal-foot {
  display: flex; justify-content: flex-end; gap: 10px;
  padding: 14px 22px; border-top: 1px solid var(--border); flex-shrink: 0;
}
</style>
