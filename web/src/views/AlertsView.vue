<script setup>
// 告警页：全部活跃告警（低余额 / 倍率超限 / 熔断开闸降级 / 站点禁用）
// 数据来自 GET /admin/alerts（与右下角弹窗同一数据源，实时计算）。
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'
import { store } from '../store'
import GroupSwitcher from '../components/GroupSwitcher.vue'
import EmptyState from '../components/EmptyState.vue'
import Icon from '../components/Icon.vue'

const router = useRouter()

const loading = ref(true)
const alerts = ref([])
const lastRefresh = ref('')
let pollTimer = null

// 告警类型 → 展示信息与处理页
const TYPE_INFO = {
  bal_:     { label: '低余额',   icon: 'key',     color: 'red',    target: '/channels' },
  ratio_:   { label: '倍率超标', icon: 'chart',   color: 'red',    target: '/channels' },
  cb_:      { label: '熔断',     icon: 'zap_off', color: 'orange', target: '/circuit' },
  dis_:     { label: '站点禁用', icon: 'server',  color: 'orange', target: '/channels' },
  pricing_: { label: '价格同步', icon: 'refresh', color: 'orange', target: '/channels' },
}

function typeOf(a) {
  if (!a?.id) return null
  const key = ['bal_', 'ratio_', 'cb_', 'dis_', 'pricing_'].find(p => a.id.startsWith(p))
  return TYPE_INFO[key] || { label: '其他', icon: 'alert', color: 'orange', target: '/' }
}

const criticalCount = computed(() => alerts.value.filter(a => a.sev === 'critical').length)
const warningCount = computed(() => alerts.value.filter(a => a.sev !== 'critical').length)

async function load() {
  loading.value = true
  try {
    const g = store.currentGroup
    const r = await api.alerts(g)
    alerts.value = r.alerts || []
    store.alerts = alerts.value
    lastRefresh.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  } catch { /* api 层已提示 */ }
  finally { loading.value = false }
}

function goHandle(a) {
  router.push(typeOf(a).target)
}

function onGroupChange() { load() }

onMounted(() => {
  load()
  pollTimer = setInterval(load, 30000)
})
onUnmounted(() => clearInterval(pollTimer))
</script>

<template>
  <div class="page-wrap fade-in">
    <!-- 页头 -->
    <div class="page-head">
      <div>
        <div class="page-title">告警</div>
        <div class="page-sub">全部活跃告警 · 实时计算{{ lastRefresh ? ' · ' + lastRefresh + ' 更新' : '' }}</div>
      </div>
      <div class="row gap-3" style="flex-wrap:wrap">
        <GroupSwitcher @change="onGroupChange" />
        <button class="btn btn-ghost" @click="load" :disabled="loading">
          <Icon name="refresh" :size="15" />刷新
        </button>
      </div>
    </div>

    <!-- 摘要卡 -->
    <div class="stat-grid mb-4">
      <div class="card stat-card">
        <div class="stat-label">全部告警</div>
        <div class="stat-value" style="color:var(--text-1)">{{ alerts.length }}</div>
      </div>
      <div class="card stat-card">
        <div class="stat-label">严重</div>
        <div class="stat-value" :style="{ color: criticalCount ? 'var(--red)' : 'var(--green)' }">{{ criticalCount }}</div>
      </div>
      <div class="card stat-card">
        <div class="stat-label">警告</div>
        <div class="stat-value" :style="{ color: warningCount ? '#ff9f0a' : 'var(--green)' }">{{ warningCount }}</div>
      </div>
      <div class="card stat-card">
        <div class="stat-label">刷新周期</div>
        <div class="stat-value" style="font-size:22px">30s</div>
      </div>
    </div>

    <!-- 告警列表 -->
    <div class="card">
      <div class="card-head">活跃告警<span class="sub">按严重度排序，处理或自动恢复后消失</span></div>
      <div v-if="loading" class="skeleton" style="height:220px;margin:0 18px 18px" />
      <EmptyState v-else-if="!alerts.length" icon="check" title="一切正常"
        desc="当前没有活跃告警。余额、倍率、熔断与站点状态的异常会实时出现在这里。" style="padding:56px 0" />
      <div v-else class="alerts-list">
        <div v-for="a in alerts" :key="a.id" class="alert-item" :class="'sev-' + (a.sev === 'critical' ? 'critical' : 'warning')">
          <span class="alert-type-icon"><Icon :name="typeOf(a).icon" :size="16" /></span>
          <div class="grow" style="min-width:0">
            <div class="row gap-2" style="align-items:center;flex-wrap:wrap">
              <span class="badge" :class="a.sev === 'critical' ? 'badge-red' : 'badge-orange'">
                {{ a.sev === 'critical' ? '严重' : '警告' }}
              </span>
              <span class="badge badge-gray">{{ typeOf(a).label }}</span>
              <span v-if="a.channel" class="text-3" style="font-size:12px">{{ a.channel }}</span>
            </div>
            <div class="alert-name">{{ a.name }}</div>
            <div class="text-3" style="font-size:11.5px">{{ a.ago && a.ago !== '—' ? a.ago + ' · ' : '' }}告警 ID: {{ a.id }}</div>
          </div>
          <button class="btn btn-ghost btn-sm" @click="goHandle(a)">处理<Icon name="chevron_right" :size="13" /></button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.stat-card { padding: 14px 18px; }
.stat-label { font-size: 11.5px; color: var(--text-3); font-weight: 600; }
.stat-value { font-size: 26px; font-weight: 700; margin-top: 4px; letter-spacing: -0.01em; }

.alerts-list { padding: 6px 18px 14px; display: flex; flex-direction: column; gap: 10px; }
.alert-item {
  display: flex; align-items: center; gap: 12px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  background: var(--surface-solid);
  transition: border-color var(--dur) var(--ease), box-shadow var(--dur) var(--ease);
}
.alert-item:hover { border-color: var(--text-3); }
.alert-item.sev-critical { border-left: 3px solid var(--red); }
.alert-item.sev-warning { border-left: 3px solid #ff9f0a; }

.alert-type-icon {
  display: flex; align-items: center; justify-content: center;
  width: 34px; height: 34px; border-radius: 10px; flex-shrink: 0;
}
.sev-critical .alert-type-icon { background: var(--red-soft); color: var(--red); }
.sev-warning .alert-type-icon { background: rgba(255, 159, 10, 0.12); color: #ff9f0a; }

.alert-name { font-size: 13.5px; font-weight: 600; margin: 4px 0 2px; line-height: 1.45; word-break: break-word; }
</style>
