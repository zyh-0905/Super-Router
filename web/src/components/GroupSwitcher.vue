<script setup>
// 分组切换器：全部 / 各分组（全局共享 store.currentGroup）
import { computed, onMounted } from 'vue'
import { store } from '../store'
import { api } from '../api'
import Icon from './Icon.vue'
import SelectBox from './SelectBox.vue'

const props = defineProps({
  compact: { type: Boolean, default: false },
})
const emit = defineEmits(['change'])

const groups = computed(() => store.groups || [])

const groupOpts = computed(() => [
  { value: null, label: '全部' },
  ...groups.value.map(g => ({ value: g.id, label: `${g.name}（${g.channel_count}）` })),
])

async function ensureGroups() {
  if (!groups.value.length) {
    try { store.groups = (await api.listGroups()).groups || [] } catch { /* 忽略 */ }
  }
}

function pick(g) {
  store.currentGroup = g ? g.id : null
  emit('change', store.currentGroup)
}

const currentName = computed(() => {
  if (store.currentGroup == null) return '全部'
  return groups.value.find(g => g.id === store.currentGroup)?.name || '—'
})

onMounted(ensureGroups)
</script>

<template>
  <div class="group-switch">
    <template v-if="compact">
      <SelectBox
        :model-value="store.currentGroup"
        :options="groupOpts"
        width="140px"
        @update:model-value="pick(groups.find(g => g.id === $event) || null)"
      />
    </template>
    <template v-else>
      <div class="row gap-2" style="flex-wrap:wrap">
        <button class="seg" :class="{ on: store.currentGroup == null }" @click="pick(null)">
          <Icon name="layers" :size="13" />全部
        </button>
        <button v-for="g in groups" :key="g.id" class="seg" :class="{ on: store.currentGroup === g.id }" @click="pick(g)">
          {{ g.name }}
          <span class="seg-count">{{ g.channel_count }}</span>
        </button>
      </div>
    </template>
    <span class="text-3" style="font-size:11.5px;margin-left:auto;white-space:nowrap" v-if="!compact && store.currentGroup != null">
      当前：{{ currentName }}
    </span>
  </div>
</template>

<style scoped>
.group-switch { display: flex; align-items: center; gap: 10px; }
.seg {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 5px 13px; border-radius: var(--radius-full);
  border: 1px solid var(--border-strong); background: var(--surface-solid);
  color: var(--text-2); font-size: 12.5px; font-weight: 500; font-family: inherit;
  cursor: pointer; transition: all var(--dur) var(--ease);
}
.seg:hover { color: var(--text-1); }
.seg.on { background: var(--blue); border-color: var(--blue); color: #fff; }
.seg-count {
  background: rgba(255,255,255,0.22); border-radius: var(--radius-full);
  padding: 0 6px; font-size: 10.5px; font-weight: 600;
}
</style>
