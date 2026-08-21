<script setup>
// 中转站视图：相同 base_url 的站点归纳为同一「中转站」卡片。
// 卡片显示账户余额（同账户成员余额相同，取最近一次成功检测）与成员数，
// 点击展开显示成员站点列表；命名可由管理员自定义（空 = 自动命名）。
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { toast } from '../store'
import Icon from './Icon.vue'
import { fmtTime } from '../utils'

const emit = defineEmits(['open-channel'])

const loading = ref(true)
const stations = ref([])
const search = ref('')
const expanded = ref(new Set())

// 成员站点筛选（默认只显示已启用；勾选显示全部）
const showDisabled = ref(false)

const filtered = computed(() => {
  const q = search.value ? search.value.toLowerCase() : ''
  return stations.value.filter(s => {
    if (q && !s.display_name.toLowerCase().includes(q) && !s.base_url.toLowerCase().includes(q)) return false
    return true
  })
})

function memberList(s) {
  const ms = s.members || []
  return showDisabled.value ? ms : ms.filter(m => m.enabled)
}

function toggle(s) {
  const set = new Set(expanded.value)
  if (set.has(s.id)) set.delete(s.id)
  else set.add(s.id)
  expanded.value = set
}

function fmtBalance(v) {
  if (v == null || isNaN(v)) return '—'
  return '$' + Number(v).toFixed(2)
}

function circuitBadge(state) {
  return {
    open: { cls: 'badge-red', label: '熔断开闸' },
    degraded: { cls: 'badge-orange', label: '熔断降级' },
    half_open: { cls: 'badge-yellow', label: '半开探测' },
    closed: { cls: 'badge-green', label: '正常' },
  }[state] || { cls: 'badge-gray', label: state || '未知' }
}

function roleLabel(r) {
  return { primary: '主力', backup: '备用', emergency: '应急' }[r] || r || ''
}

// 重命名
const renaming = ref(null) // 正在重命名的站点对象
const renameInput = ref('')
const renameSaving = ref(false)

function startRename(s, ev) {
  ev.stopPropagation()
  renaming.value = s
  renameInput.value = s.custom_name ? s.display_name : ''
}

function cancelRename() {
  renaming.value = null
  renameInput.value = ''
}

async function saveRename(s) {
  if (renameSaving.value) return
  renameSaving.value = true
  try {
    await api.renameRelayStation(s.id, renameInput.value.trim())
    toast('中转站名称已更新', 'success')
    await load()
    renaming.value = null
  } catch { /* api 层已提示 */ }
  finally { renameSaving.value = false }
}

async function load() {
  loading.value = true
  try {
    const r = await api.relayStations()
    stations.value = r.stations || []
  } catch { stations.value = [] }
  finally { loading.value = false }
}

onMounted(load)
</script>

<template>
  <div class="col gap-2">
    <div class="row gap-2 mb-1">
      <div class="grow" style="position:relative">
        <span style="position:absolute;left:11px;top:50%;transform:translateY(-50%);color:var(--text-3);display:flex"><Icon name="search" :size="14" /></span>
        <input v-model="search" class="input" placeholder="搜索中转站 / Base URL" style="padding-left:33px">
      </div>
      <label class="row gap-1" style="align-items:center;font-size:12.5px;color:var(--text-2);cursor:pointer;flex-shrink:0">
        <input type="checkbox" v-model="showDisabled" style="accent-color:var(--accent)"> 显示已禁用成员
      </label>
    </div>

    <div v-if="loading" class="col" style="gap:10px">
      <div v-for="i in 3" :key="i" class="card skeleton" style="height:76px" />
    </div>

    <div v-else-if="filtered.length === 0" class="card" style="padding:40px;text-align:center;color:var(--text-3)">
      暂无中转站（添加站点后自动按 Base URL 归并）
    </div>

    <div v-for="s in filtered" :key="s.id" class="card st-card" :class="{ open: expanded.has(s.id) }" @click="toggle(s)">
      <!-- 卡片头 -->
      <div class="row gap-2" style="align-items:center">
        <Icon name="chevron_right" :size="14" class="st-chevron" :class="{ down: expanded.has(s.id) }" style="color:var(--text-3);flex-shrink:0" />
        <template v-if="renaming?.id === s.id">
          <input
            v-model="renameInput" class="input" style="max-width:260px;font-weight:600"
            placeholder="留空 = 自动命名" @click.stop @keyup.enter="saveRename(s)" @keyup.esc="cancelRename"
          />
          <button class="btn btn-primary btn-sm" :disabled="renameSaving" @click.stop="saveRename(s)">保存</button>
          <button class="btn btn-ghost btn-sm" @click.stop="cancelRename">取消</button>
        </template>
        <template v-else>
          <span style="font-size:14.5px;font-weight:600" class="truncate">{{ s.display_name }}</span>
          <span v-if="s.custom_name" class="badge badge-teal" style="font-size:10px">自定义名</span>
          <button class="btn btn-ghost btn-sm" style="padding:2px 8px" title="重命名" @click.stop="startRename(s, $event)"><Icon name="pencil" :size="12" /></button>
        </template>

        <span class="st-balance mono">
          💰 {{ fmtBalance(s.balance) }}
          <span v-if="s.balance_checked_at" class="text-3" style="font-size:10.5px;font-weight:400" :title="'检测于 ' + fmtTime(s.balance_checked_at)">{{ fmtTime(s.balance_checked_at) }}</span>
        </span>
        <span class="badge badge-gray mono" style="margin-left:auto">{{ s.enabled_count }}/{{ s.channel_count }} 启用</span>
      </div>
      <div class="text-3 mono" style="font-size:11px;margin-top:2px">{{ s.base_url }}</div>

      <!-- 展开成员列表 -->
      <div v-if="expanded.has(s.id)" class="st-members" @click.stop>
        <div v-if="memberList(s).length === 0" class="text-3" style="font-size:12.5px;padding:8px 4px">
          {{ (s.members || []).length === 0 ? '暂无成员站点' : '成员站点均已禁用（勾选「显示已禁用成员」查看）' }}
        </div>
        <div
          v-for="m in memberList(s)" :key="m.channel_id"
          class="st-member row gap-2" style="align-items:center"
          @click="emit('open-channel', m.channel_id)"
        >
          <span style="width:8px;height:8px;border-radius:50%;flex-shrink:0" :style="{ background: m.healthy === true ? 'var(--green)' : m.healthy === false ? 'var(--red)' : 'var(--text-3)' }" :title="m.healthy == null ? '健康未知' : m.healthy ? '存活' : '离线'" />
          <span class="grow truncate" style="font-size:13px">{{ m.name }}</span>
          <span v-if="m.protocol" class="badge" :class="m.protocol === 'anthropic' ? 'badge-purple' : 'badge-blue'" style="font-size:10px">{{ m.protocol === 'anthropic' ? 'Anthropic' : 'OpenAI' }}</span>
          <span class="badge" :class="{ primary: 'badge-blue', backup: 'badge-gray', emergency: 'badge-orange' }[m.role] || 'badge-gray'" style="font-size:10px">{{ roleLabel(m.role) }}</span>
          <span v-if="m.ratio != null" class="badge mono badge-teal" style="font-size:10px" :title="'模型 ' + m.ratio_model + ' · ' + (m.ratio_basis === 'official' ? '官网价基准' : '混合基准')">{{ Number(m.ratio).toFixed(2) }}x</span>
          <span class="badge" :class="circuitBadge(m.circuit_state).cls" style="font-size:10px">{{ circuitBadge(m.circuit_state).label }}</span>
          <span class="badge" :class="m.enabled ? 'badge-green' : 'badge-gray'" style="font-size:10px">{{ m.enabled ? '启用' : '禁用' }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.st-card { cursor: pointer; padding: 12px 14px; transition: box-shadow .15s ease; }
.st-card:hover { box-shadow: var(--shadow); }
.st-card.open { box-shadow: var(--shadow); border-color: var(--accent); }
.st-chevron { transition: transform .15s ease; }
.st-chevron.down { transform: rotate(90deg); }.st-balance { font-size: 13px; font-weight: 600; color: var(--text-1); }
.st-members { margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 4px; }
.st-member { padding: 7px 8px; border-radius: 8px; cursor: pointer; }
.st-member:hover { background: var(--bg-2); }
</style>
