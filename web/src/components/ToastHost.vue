<script setup>
import { store } from '../store'
import Icon from './Icon.vue'

function dismissToast(id) {
  const i = store.toasts.findIndex(t => t.id === id)
  if (i >= 0) store.toasts.splice(i, 1)
}
</script>

<template>
  <Teleport to="body">
    <div class="toast-host" role="status" aria-live="polite">
      <TransitionGroup name="toast">
        <div v-for="t in store.toasts" :key="t.id" class="toast" :class="'toast-' + t.type">
          <Icon :name="t.type === 'success' ? 'check' : t.type === 'error' ? 'alert' : 'dot'" :size="15" aria-hidden="true" />
          <span class="toast-msg">{{ t.message }}</span>
          <button class="toast-close" :aria-label="'关闭通知'" @click="dismissToast(t.id)">
            <Icon name="x" :size="12" />
          </button>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.toast-host {
  position: fixed; top: 16px; right: 16px; z-index: var(--z-toast);
  display: flex; flex-direction: column; gap: 8px; pointer-events: none;
}
.toast {
  display: flex; align-items: center; gap: 9px;
  min-width: 240px; max-width: 400px;
  padding: 11px 16px;
  background: var(--surface-raised);
  backdrop-filter: saturate(180%) blur(28px);
  -webkit-backdrop-filter: saturate(180%) blur(28px);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-raised);
  font-size: 13px; font-weight: 500;
  pointer-events: all;
}
.toast-success { color: var(--green); }
.toast-error { color: var(--red); }
.toast-info { color: var(--text-1); }
.toast-msg { color: var(--text-1); font-weight: 500; flex: 1; }
.toast-error .toast-msg { color: var(--red); }
.toast-success .toast-msg { color: var(--green); }

.toast-close {
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: 50%;
  border: none; background: transparent; color: var(--text-3);
  cursor: pointer; flex-shrink: 0; margin-left: auto;
  transition: color 0.15s, background 0.15s;
}
.toast-close:hover { color: var(--text-1); background: var(--border); }

.toast-enter-active, .toast-leave-active { transition: all 0.3s cubic-bezier(0.32, 0.72, 0, 1); }
.toast-enter-from { opacity: 0; transform: translateX(20px); }
.toast-leave-to { opacity: 0; transform: translateX(20px); }
</style>
