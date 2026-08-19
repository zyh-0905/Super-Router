<script setup>
// 通用确认对话框 — 替代原生 confirm()，风格与整体一致
import BaseModal from './BaseModal.vue'
import Icon from './Icon.vue'

defineProps({
  title: { type: String, default: '确认操作' },
  message: { type: String, required: true },
  confirmText: { type: String, default: '确认' },
  cancelText: { type: String, default: '取消' },
  danger: { type: Boolean, default: false },
})
const emit = defineEmits(['confirm', 'cancel'])
</script>

<template>
  <BaseModal :title="title" width="380px" @close="emit('cancel')">
    <div class="confirm-body">
      <div class="confirm-icon" :class="{ danger }">
        <Icon :name="danger ? 'alert' : 'check'" :size="20" />
      </div>
      <p class="confirm-msg">{{ message }}</p>
    </div>
    <template #footer>
      <button class="btn btn-ghost" @click="emit('cancel')">{{ cancelText }}</button>
      <button
        class="btn"
        :class="danger ? 'btn-danger' : 'btn-primary'"
        @click="emit('confirm')"
        autofocus
      >
        {{ confirmText }}
      </button>
    </template>
  </BaseModal>
</template>

<style scoped>
.confirm-body {
  display: flex; align-items: flex-start; gap: 14px;
}
.confirm-icon {
  width: 38px; height: 38px; border-radius: 10px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  background: var(--blue-soft); color: var(--blue);
}
.confirm-icon.danger {
  background: var(--red-soft); color: var(--red);
}
.confirm-msg {
  font-size: 14px; line-height: 1.6; color: var(--text-1);
  padding-top: 6px;
}
</style>
