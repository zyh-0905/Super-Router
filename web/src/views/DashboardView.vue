<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'
import { store, toast } from '../store'
import StatCard from '../components/StatCard.vue'
import BaseChart from '../components/BaseChart.vue'
import EmptyState from '../components/EmptyState.vue'
import GroupSwitcher from '../components/GroupSwitcher.vue'
import Icon from '../components/Icon.vue'
import { fmtTime, fmtNum, fmtMs, fmtPct, fmtAgo } from '../utils'

const router = useRouter()
const loading = ref(true)
const stats = ref(null)
const decisions = ref([])
const lastRefresh = ref('')

const totals = computed(() => stats.value?.totals || {})

// ---- 同比指标 ----
const reqDelta = computed(() => {
  const cur = totals.value.requests_24h || 0
  const prev = totals.value.requests_prev_24h || 0
  if (!prev) return { txt: '暂无昨日数据', cls: 'delta-flat' }
  const d = ((cur - prev) / prev) * 100
  return { txt: `${d >= 0 ? '↑' : '↓'} ${Math.abs(d).toFixed(1)}%`, cls: d >= 0 ? 'delta-up' : 'delta-dn' }
})

const srDelta = computed(() => {
  const cur = (totals.value.success_rate_24h || 0) * 100
  const prev = (totals.value.success_rate_prev || 0) * 100
  if (!prev) return { txt: '暂无昨日数据', cls: 'delta-flat' }
  const d = cur - prev
  return { txt: `${d >= 0 ? '↑' : '↓'} ${Math.abs(d).toFixed(1)}%`, cls: d >= 0 ? 'delta-up' : 'delta-dn' }
})

const latDelta = computed(() => {
  const cur = totals.value.avg_latency_ms_24h || 0
  const prev = totals.value.avg_latency_prev || 0
  if (!prev) return { txt: '暂无昨日数据', cls: 'delta-flat' }
  const d = Math.round(cur - prev)
  return { txt: `${d <= 0 ? '↓' : '↑'} ${Math.abs(d)}ms`, cls: d <= 0 ? 'delta-up' : 'delta-dn' }
})

const srColor = computed(() => {
  const v = (totals.value.success_rate_24h || 0) * 100
  return v > 98 ? 'var(--green)' : v > 90 ? 'var(--orange)' : 'var(--red)'
})

// ---- 图表 ----
const trendOption = computed(() => ({
  grid: { left: 42, right: 12, top: 30, bottom: 26 },
  legend: { top: 0, right: 0, itemWidth: 14, itemHeight: 8, textStyle: { color: '#86868b', fontSize: 11 } },
  xAxis: { type: 'category', data: (stats.value?.trend || []).map(t => t.hour), axisLabel: { interval: 3, fontSize: 10 } },
  yAxis: { type: 'value' },
  series: [
    {
      name: '请求', type: 'line', smooth: true, symbol: 'none',
      data: (stats.value?.trend || []).map(t => t.count),
      lineStyle: { color: '#0a84ff', width: 2.2 },
      areaStyle: { color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [{ offset: 0, color: 'rgba(10,132,255,0.16)' }, { offset: 1, color: 'rgba(10,132,255,0)' }] } },
    },
    {
      name: '失败', type: 'line', smooth: true, symbol: 'none',
      data: (stats.value?.trend || []).map(t => t.failed),
      lineStyle: { color: '#ff453a', width: 1.4, type: 'dashed' },
    },
  ],
}))

const modelOption = computed(() => {
  const palette = ['#0a84ff', '#30d158', '#ff9f0a', '#bf5af2', '#ff375f', '#64d2ff', '#ffd60a', '#ac8e68']
  return {
    legend: { orient: 'vertical', right: 0, top: 'center', textStyle: { color: '#86868b', fontSize: 11 } },
    series: [{
      type: 'pie',
      radius: ['48%', '74%'],
      center: ['38%', '50%'],
      itemStyle: { borderColor: 'transparent', borderRadius: 4 },
      label: { show: false },
      data: (stats.value?.models || []).map((m, i) => ({ value: m.count, name: m.name, itemStyle: { color: palette[i % palette.length] } })),
    }],
  }
})

// ---- 告警 ----
const alertIcon = a => (a.sev === 'critical' ? 'alert' : 'alert')

async function load() {
  loading.value = true
  try {
    const [s, d] = await Promise.all([api.stats(store.currentGroup), api.decisions(10, store.currentGroup)])
    stats.value = s
    store.stats = s
    store.groups = s.groups || []
    store.alerts = s.alerts || []
    store.epoch = s.epoch != null ? String(s.epoch) : null
    decisions.value = d.decisions || []
  } catch { /* 错误已由 api 层 toast */ }
  finally {
    loading.value = false
    lastRefresh.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  }
}

function onGroupChange() { load() }

onMounted(load)
</script>

<template>
  <div class="page-wrap fade-in">
    <!-- 页头 -->
    <div class="page-head">
      <div>
        <div class="page-title">总览</div>
        <div class="page-sub">最近 24 小时的路由运行状况{{ lastRefresh ? ' · ' + lastRefresh + ' 更新' : '' }}</div>
      </div>
      <div class="row gap-3" style="flex-wrap:wrap">
        <GroupSwitcher @change="onGroupChange" />
        <button class="btn btn-ghost" @click="load" :disabled="loading">
          <Icon name="refresh" :size="15" />刷新
        </button>
      </div>
    </div>

    <!-- 统计卡 -->
    <div class="stat-grid mb-4">
      <StatCard label="总请求数" :value="fmtNum(totals.requests_24h)" unit="次" icon="chart" color="var(--blue)" clickable @click="router.push('/decisions')">
        <template #foot><span :class="reqDelta.cls">{{ reqDelta.txt }}</span><span>较昨日</span></template>
      </StatCard>
      <StatCard label="成功率" :value="fmtPct(totals.success_rate_24h)" icon="check" :color="srColor">
        <template #foot><span :class="srDelta.cls">{{ srDelta.txt }}</span><span>较昨日</span></template>
      </StatCard>
      <StatCard label="平均延迟" :value="fmtMs(totals.avg_latency_ms_24h)" icon="gauge" color="var(--orange)">
        <template #foot><span :class="latDelta.cls">{{ latDelta.txt }}</span><span>较昨日</span></template>
      </StatCard>
      <StatCard label="活跃站点" :value="stats?.active_channels ?? '—'" :unit="stats != null ? '/ ' + stats.total_channels : ''" icon="server" color="var(--green)" clickable @click="router.push('/channels')">
        <template #foot>
          <span :class="(stats?.alerts || []).length ? 'delta-dn' : 'delta-up'">{{ (stats?.alerts || []).length }} 条告警</span>
          <span>· 熔断与站点</span>
        </template>
      </StatCard>
    </div>

    <!-- 图表行 -->
    <div class="grid-2 mb-4">
      <div class="card">
        <div class="card-head">请求趋势<span class="sub">24h · 每小时</span></div>
        <div class="card-pad" style="padding-top:14px">
          <BaseChart v-if="!loading && (stats?.trend || []).length" :option="trendOption" height="210px" />
          <div v-else-if="loading" class="skeleton" style="height:210px" />
          <EmptyState v-else icon="chart" title="暂无请求数据" desc="在「测试台」发送一个请求后，这里会出现趋势曲线" style="padding:36px 0" />
        </div>
      </div>
      <div class="card">
        <div class="card-head">模型分布<span class="sub">24h</span></div>
        <div class="card-pad" style="padding-top:14px">
          <BaseChart v-if="!loading && (stats?.models || []).length" :option="modelOption" height="210px" />
          <div v-else-if="loading" class="skeleton" style="height:210px" />
          <EmptyState v-else icon="layers" title="暂无模型数据" style="padding:36px 0" />
        </div>
      </div>
    </div>

    <!-- 告警 + 最近决策 -->
    <div class="grid-2">
      <div class="card">
        <div class="card-head">告警<span class="sub" :class="(stats?.alerts || []).length ? 'text-red' : 'text-green'">
          {{ (stats?.alerts || []).length ? (stats.alerts.length + ' 条活跃') : '全部正常' }}</span>
        </div>
        <div class="card-pad">
          <div v-if="(stats?.alerts || []).length === 0" class="row gap-2" style="color:var(--green)">
            <Icon name="check" :size="16" /><span style="font-size:13.5px">系统运行正常，没有活跃告警。</span>
          </div>
          <div v-for="a in stats?.alerts || []" :key="a.id" class="row gap-3 alert-row">
            <Icon :name="a.sev === 'critical' ? 'zap_off' : 'alert'" :size="16" :style="{ color: a.sev === 'critical' ? 'var(--red)' : 'var(--orange)' }" />
            <div class="grow">
              <div style="font-size:13.5px;font-weight:500">{{ a.name }}</div>
              <div class="text-3" style="font-size:12px">{{ a.channel }} · {{ fmtAgo(a.ago === '—' ? null : a.ago) }}</div>
            </div>
            <router-link v-if="a.name.includes('熔断')" to="/circuit" class="btn btn-ghost btn-sm">查看</router-link>
            <router-link v-else to="/channels" class="btn btn-ghost btn-sm">查看</router-link>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-head">最近决策
          <span class="sub" />
          <router-link to="/decisions" class="btn btn-ghost btn-sm">全部<Icon name="chevron_right" :size="13" /></router-link>
        </div>
        <div class="card-pad" style="padding:8px 0 10px">
          <div v-if="decisions.length === 0" class="text-3" style="padding:18px 22px;font-size:13px">暂无决策记录</div>
          <div v-for="d in decisions.slice(0, 5)" :key="d.request_id" class="row gap-3 dec-row" @click="router.push({ path: '/decisions', query: { id: d.request_id } })">
            <span class="mono text-3 nowrap">{{ fmtTime(d.decided_at) }}</span>
            <span class="badge badge-blue">{{ d.model }}</span>
            <Icon name="arrow_right" :size="13" style="color:var(--text-3)" />
            <span class="grow truncate" style="font-size:13.5px">{{ d.selected_channel || '—' }}</span>
            <span class="badge badge-purple">{{ d.strategy || d.policy_version || '—' }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.alert-row { padding: 9px 0; border-bottom: 1px solid var(--border); }
.alert-row:last-child { border-bottom: none; }
.alert-row:first-child { padding-top: 2px; }
.dec-row { padding: 9px 22px; cursor: pointer; transition: background var(--dur) var(--ease); }
.dec-row:hover { background: var(--surface-hover); }
</style>
