<script setup>
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { store, toast } from '../store'
import StatCard from '../components/StatCard.vue'
import EmptyState from '../components/EmptyState.vue'
import GroupSwitcher from '../components/GroupSwitcher.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import Icon from '../components/Icon.vue'
import { fmtDate } from '../utils'

const loading = ref(true)
const states = ref([])

const counts = computed(() => {
  const c = { closed: 0, half_open: 0, degraded: 0, open: 0 }
  states.value.forEach(s => { c[s.state] = (c[s.state] || 0) + 1 })
  return c
})

function badge(state) {
  const map = { closed: 'badge-green', half_open: 'badge-orange', degraded: 'badge-orange', open: 'badge-red' }
  const label = { closed: 'CLOSED · 正常', half_open: 'HALF_OPEN · 半开', degraded: 'DEGRADED · 降级', open: 'OPEN · 熔断' }
  return { cls: map[state] || 'badge-gray', label: label[state] || state }
}

const stateFlow = ['closed', 'open', 'half_open', 'degraded']

async function load() {
  loading.value = true
  try {
    const r = await api.circuit(store.currentGroup)
    states.value = r.states || []
  } catch { /* 已提示 */ }
  finally { loading.value = false }
}

function onGroupChange() { load() }

// 确认对话框
const confirmReset = ref(null) // { channel_name, model, channel_id }

function askReset(s) {
  confirmReset.value = s
}

async function doReset() {
  const s = confirmReset.value
  confirmReset.value = null
  try {
    await api.resetCircuit(s.channel_id)
    toast('熔断器已重置', 'success')
    await load()
  } catch { /* 已提示 */ }
}

onMounted(load)
</script>

<template>
  <div class="page-wrap fade-in">
    <div class="page-head">
      <div>
        <div class="page-title">熔断</div>
        <div class="page-sub">四态熔断器 · 保护上游渠道不被雪崩式故障拖垮</div>
      </div>
      <div class="row gap-3" style="flex-wrap:wrap">
        <GroupSwitcher @change="onGroupChange" />
        <button class="btn btn-ghost" @click="load" :disabled="loading"><Icon name="refresh" :size="15" />刷新</button>
      </div>
    </div>

    <!-- 四态统计 -->
    <div class="stat-grid mb-4">
      <StatCard label="CLOSED" :value="counts.closed" unit="正常" icon="check" color="var(--green)" />
      <StatCard label="HALF_OPEN" :value="counts.half_open" unit="探测中" icon="gauge" color="var(--orange)" />
      <StatCard label="DEGRADED" :value="counts.degraded" unit="降级" icon="alert" color="var(--orange)" />
      <StatCard label="OPEN" :value="counts.open" unit="已熔断" icon="zap_off" color="var(--red)" />
    </div>

    <!-- 状态机说明 -->
    <div class="card mb-4" style="padding:18px 22px">
      <div class="row gap-2" style="flex-wrap:wrap">
        <span class="text-3" style="font-size:12px;margin-right:6px">状态流转</span>
        <span v-for="(s, i) in stateFlow" :key="s" class="row gap-2">
          <span class="badge" :class="badge(s).cls">{{ s.toUpperCase() }}</span>
          <Icon v-if="i < stateFlow.length - 1" name="arrow_right" :size="13" style="color:var(--text-3)" />
        </span>
        <span class="text-3" style="font-size:12px;margin-left:6px">· 熔断开启后指数退避冷却（30s → 600s），半开期放行探测请求，连续成功逐步恢复</span>
      </div>
    </div>

    <!-- 熔断列表 -->
    <div class="card table-wrap">
      <table>
        <thead><tr><th scope="col">渠道</th><th scope="col">模型</th><th scope="col">状态</th><th scope="col">失败 / 成功</th><th scope="col">冷却截止</th><th scope="col">更新时间</th><th scope="col"><span class="sr-only">操作</span></th></tr></thead>
        <tbody>
          <tr v-if="loading"><td colspan="7"><div class="skeleton" style="height:22px;margin:10px 0" /></td></tr>
          <tr v-if="!loading && states.length === 0">
            <td colspan="7"><EmptyState icon="bolt" title="暂无熔断记录" desc="请求量达到阈值后，熔断器会自动创建状态记录" style="padding:40px 0" /></td>
          </tr>
          <tr v-for="s in states" :key="s.channel_id + '_' + s.model">
            <td style="font-weight:600" data-label="渠道">{{ s.channel_name }}</td>
            <td data-label="模型"><span class="badge badge-blue">{{ s.model }}</span></td>
            <td data-label="状态">
              <span class="badge" :class="badge(s.state).cls">{{ badge(s.state).label }}</span>
            </td>
            <td class="mono" data-label="失败/成功"><span class="text-red">{{ s.failure_count }}</span> / <span class="text-green">{{ s.success_count }}</span></td>
            <td class="mono text-3" data-label="冷却截止">{{ s.cooling_until ? fmtDate(s.cooling_until) : '—' }}</td>
            <td class="mono text-3" data-label="更新时间">{{ fmtDate(s.updated_at) }}</td>
            <td style="width:90px">
              <button class="btn btn-ghost btn-sm" :disabled="s.state === 'closed'" @click="askReset(s)">重置</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 重置确认 -->
    <ConfirmDialog
      v-if="confirmReset"
      title="重置熔断器"
      :message="`确认重置「${confirmReset.channel_name}」(${confirmReset.model}) 的熔断状态？`"
      confirm-text="重置"
      danger
      @confirm="doReset"
      @cancel="confirmReset = null"
    />
  </div>
</template>
