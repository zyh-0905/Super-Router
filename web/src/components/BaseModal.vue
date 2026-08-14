<script setup>
// 通用弹窗：毛玻璃遮罩 + 居中卡片
defineProps({
  title: String,
  width: { type: String, default: '520px' },
  showClose: { type: Boolean, default: true },
})
const emit = defineEmits(['close'])
</script>

<template>
  <Teleport to="body">
    <div class="modal-overlay" @mousedown.self="emit('close')">
      <div class="modal-sheet" :style="{ width }" role="dialog">
        <div class="modal-head">
          <span class="modal-title">{{ title }}</span>
          <button v-if="showClose" class="icon-btn" style="width:28px;height:28px" @click="emit('close')">
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
