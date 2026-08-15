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
import { fmtTime, fmtNum, fmtMs, fmtPct, fmtAgo, fmtDate } from '../utils'

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

// ---- 站点综合信息（抽屉叠放）----
const metricsStackEl = ref(null)
const currentRatioIdx = ref(0)
const probingRatioChannel = ref(null)
const metrics = ref(null)
const metricChannels = computed(() => metrics.value?.channels || [])

function onRatioScroll() {
  const el = metricsStackEl.value
  if (!el) return
  const idx = Math.round(el.scrollTop / el.clientHeight)
  currentRatioIdx.value = Math.max(0, Math.min(idx, metricChannels.value.length - 1))
}

function scrollRatioTo(i) {
  const el = metricsStackEl.value
  if (!el) return
  const idx = Math.max(0, Math.min(i, metricChannels.value.length - 1))
  el.scrollTo({ top: idx * el.clientHeight, behavior: 'smooth' })
  currentRatioIdx.value = idx
}

async function probeChannelRatio(ch) {
  if (probingRatioChannel.value) return
  if (!ch.default_probe_model) { toast('该站点无可用模型，无法实测', 'error'); return }
  probingRatioChannel.value = ch.id
  try {
    await api.probeRatio(ch.id, ch.default_probe_model, 64)
    toast(`「${ch.name}」实测完成`, 'success')
    await load()
  } catch { /* api 层已提示 */ }
  finally { probingRatioChannel.value = null }
}

// ---- 五个迷你图 option 构建器 ----

// 倍率：各模型实测倍率横向条形图（超限红色）
function ratioBarOption(ch) {
  const data = [...(ch.ratios || [])]
    .map(mr => ({
      value: Number(mr.real_ratio),
      model: mr.model,
      source: mr.source,
      checked: mr.checked_at,
      over: ch.ratio_limit > 0 && Number(mr.real_ratio) > Number(ch.ratio_limit),
      itemStyle: { color: ch.ratio_limit > 0 && Number(mr.real_ratio) > Number(ch.ratio_limit) ? '#ff453a' : '#0a84ff', borderRadius: 4 },
    }))
    .sort((a, b) => b.value - a.value)
  return {
    grid: { left: 86, right: 14, top: 8, bottom: 18 },
    xAxis: { type: 'value', axisLabel: { fontSize: 9, formatter: '{value}x' } },
    yAxis: { type: 'category', data: data.map(d => d.model), axisLabel: { fontSize: 9.5, width: 80, overflow: 'truncate' } },
    tooltip: {
      trigger: 'item',
      formatter: p => {
        const d = p.data
        return `${d.model}<br/>实测倍率: <b>${Number(d.value).toFixed(4)}x</b><br/>单价: $${(d.value * 10).toFixed(2)}/1M<br/>来源: ${d.source === 'manual' ? '手动' : '定时'}<br/>时间: ${fmtDate(d.checked)}`
      },
    },
    series: [{ type: 'bar', data, barMaxWidth: 11 }],
  }
}

// 余额：24h 折线
function balanceLineOption(ch) {
  const series = ch.balance_series || []
  return {
    grid: { left: 46, right: 12, top: 8, bottom: 18 },
    xAxis: { type: 'category', data: series.map(s => s.t.slice(5, 16).replace('T', ' ')), axisLabel: { fontSize: 9, interval: Math.max(0, Math.floor(series.length / 6)) } },
    yAxis: { type: 'value', axisLabel: { fontSize: 9, formatter: '${value}' } },
    tooltip: {
      trigger: 'axis',
      formatter: ps => {
        const p = Array.isArray(ps) ? ps[0] : ps
        const s = series[p?.dataIndex]
        return s ? `${fmtDate(s.t)}<br/>余额: <b>$${Number(s.v).toFixed(4)}</b>` : ''
      },
    },
    series: [{
      type: 'line', smooth: true, symbol: 'none', data: series.map(s => s.v),
      lineStyle: { color: '#30d158', width: 1.8 },
      areaStyle: { color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [{ offset: 0, color: 'rgba(48,209,88,0.15)' }, { offset: 1, color: 'rgba(48,209,88,0)' }] } },
    }],
  }
}

// 成功率 / 延迟：24h 小时桶折线
function hourlyOption(ch, kind) {
  const hourly = ch.hourly || []
  const isRate = kind === 'rate'
  const data = hourly.map(h => {
    if (isRate) return h.success_rate != null ? Number((h.success_rate * 100).toFixed(1)) : null
    return h.latency_ms != null ? Math.round(h.latency_ms) : null
  })
  return {
    grid: { left: 42, right: 10, top: 8, bottom: 18 },
    xAxis: { type: 'category', data: hourly.map(h => h.hour), axisLabel: { fontSize: 9, interval: 5 } },
    yAxis: { type: 'value', axisLabel: { fontSize: 9, formatter: isRate ? '{value}%' : '{value}ms' } },
    tooltip: {
      trigger: 'axis',
      formatter: ps => {
        const p = Array.isArray(ps) ? ps[0] : ps
        const h = hourly[p?.dataIndex]
        if (!h) return ''
        return isRate
          ? `${h.hour}<br/>成功率: <b>${h.success_rate != null ? (h.success_rate * 100).toFixed(1) + '%' : '—'}</b><br/>请求: ${h.count}`
          : `${h.hour}<br/>平均延迟: <b>${h.latency_ms != null ? Math.round(h.latency_ms) + 'ms' : '—'}</b><br/>请求: ${h.count}`
      },
    },
    series: [{
      type: 'line', smooth: true, symbol: 'none', connectNulls: false, data,
      lineStyle: { color: isRate ? '#0a84ff' : '#ff9f0a', width: 1.8 },
    }],
  }
}

// 健康：最近 50 次存活探测点阵条（绿=存活 红=离线）
function healthBarOption(ch) {
  const health = ch.health || []
  const data = health.map(h => ({
    value: 1,
    alive: h.alive,
    time: h.checked_at,
    lat: h.latency_ms,
    itemStyle: { color: h.alive ? '#30d158' : '#ff453a', borderRadius: 2 },
  }))
  return {
    grid: { left: 8, right: 8, top: 6, bottom: 6 },
    xAxis: { type: 'category', show: false, data: health.map((_, i) => i) },
    yAxis: { type: 'value', show: false, min: 0, max: 1 },
    tooltip: {
      trigger: 'item',
      formatter: p => {
        const d = p.data
        return `${fmtDate(d.time)}<br/>${d.alive ? '✓ 存活' : '✗ 离线'}${d.lat != null ? `<br/>延迟: ${d.lat}ms` : ''}`
      },
    },
    series: [{ type: 'bar', data, barWidth: '55%' }],
  }
}

async function load() {
  loading.value = true
  try {
    const [s, d, m] = await Promise.all([
      api.stats(store.currentGroup),
      api.decisions(10, store.currentGroup),
      api.channelMetrics(store.currentGroup),
    ])
    stats.value = s
    store.stats = s
    store.groups = s.groups || []
    store.alerts = s.alerts || []
    store.epoch = s.epoch != null ? String(s.epoch) : null
    decisions.value = d.decisions || []
    metrics.value = m
    currentRatioIdx.value = 0
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

    <!-- 站点综合信息（抽屉叠放） -->
    <div class="card mb-4">
      <div class="card-head" style="flex-wrap:wrap;gap:8px">
        站点综合信息
        <span class="sub">倍率 · 余额 · 健康 · 成功率 · 延迟（24h）</span>
        <span class="spacer" />
        <button class="btn btn-ghost btn-sm" :disabled="!metricChannels.length || currentRatioIdx <= 0" @click="scrollRatioTo(currentRatioIdx - 1)">
          <Icon name="chevron_up" :size="13" />
        </button>
        <span class="mono text-3" style="font-size:12px;min-width:52px;text-align:center">
          {{ metricChannels.length ? (currentRatioIdx + 1) + ' / ' + metricChannels.length : '0 / 0' }}
        </span>
        <button class="btn btn-ghost btn-sm" :disabled="!metricChannels.length || currentRatioIdx >= metricChannels.length - 1" @click="scrollRatioTo(currentRatioIdx + 1)">
          <Icon name="chevron_down" :size="13" />
        </button>
      </div>
      <div v-if="loading" class="skeleton" style="height:430px;margin:0 18px 18px" />
      <EmptyState v-else-if="metricChannels.length === 0" icon="server" title="暂无站点数据"
        desc="添加站点并完成一次实测后，这里会显示每个站点的倍率、余额、健康、成功率与延迟" style="padding:48px 0" />
      <div v-else ref="metricsStackEl" class="ratio-stack" style="height:448px" @scroll="onRatioScroll">
        <div v-for="ch in metricChannels" :key="ch.id" class="ratio-drawer">
          <div class="row gap-2" style="align-items:center;flex-wrap:wrap">
            <span class="dot" :class="!ch.enabled ? 'dot dot-gray' : ch.over_limit ? 'dot dot-red dot-pulse' : 'dot dot-green'" />
            <span style="font-size:14.5px;font-weight:700">{{ ch.name }}</span>
            <span class="badge" :class="ch.enabled ? 'badge-green' : 'badge-gray'">{{ ch.enabled ? '已启用' : '已禁用' }}</span>
            <span v-if="ch.over_limit" class="badge badge-red">倍率超上限</span>
            <span v-if="ch.balance_current" class="badge mono" :class="Number(ch.balance_current.balance) <= 1 ? 'badge-red' : 'badge-green'">
              💰 ${{ Number(ch.balance_current.balance).toFixed(2) }}
            </span>
            <span class="badge badge-gray mono">上限 {{ ch.ratio_limit > 0 ? Number(ch.ratio_limit).toFixed(4) + 'x' : '不限' }}</span>
            <span class="spacer" />
            <button class="btn btn-ghost btn-sm" :disabled="probingRatioChannel != null || !ch.default_probe_model" @click="probeChannelRatio(ch)">
              <Icon name="bolt" :size="12" />{{ probingRatioChannel === ch.id ? '实测中…' : '立即实测 ' + (ch.default_probe_model || '') }}
            </button>
            <router-link :to="{ path: '/channels', query: { select: ch.id } }" class="btn btn-ghost btn-sm">详情</router-link>
          </div>
          <div class="metric-grid">
            <div class="metric-cell">
              <div class="metric-title">倍率（各模型实测，悬停看详情）</div>
              <BaseChart v-if="(ch.ratios || []).length" :option="ratioBarOption(ch)" height="112px" />
              <div v-else class="text-3 metric-empty">暂无实测数据，点击「立即实测」</div>
            </div>
            <div class="metric-cell">
              <div class="metric-title">余额（24h）</div>
              <BaseChart v-if="(ch.balance_series || []).length" :option="balanceLineOption(ch)" height="112px" />
              <div v-else class="text-3 metric-empty">暂无余额记录</div>
            </div>
            <div class="metric-cell">
              <div class="metric-title">成功率（24h · 每小时）</div>
              <BaseChart :option="hourlyOption(ch, 'rate')" height="112px" />
            </div>
            <div class="metric-cell">
              <div class="metric-title">延迟（24h · 每小时）</div>
              <BaseChart :option="hourlyOption(ch, 'lat')" height="112px" />
            </div>
            <div class="metric-cell metric-cell-full">
              <div class="metric-title">健康（最近 50 次存活探测，悬停看时间与延迟）</div>
              <BaseChart v-if="(ch.health || []).length" :option="healthBarOption(ch)" height="58px" />
              <div v-else class="text-3 metric-empty">暂无存活探测记录</div>
            </div>
          </div>
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

/* 站点综合信息：抽屉叠放（滚轮吸附切换） */
.ratio-stack {
  height: 448px;
  overflow-y: auto;
  scroll-snap-type: y mandatory;
  border-top: 1px solid var(--border);
  scroll-behavior: smooth;
}
.ratio-drawer {
  scroll-snap-align: start;
  scroll-snap-stop: always;
  height: 100%;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.ratio-drawer:last-child { border-bottom: none; }

/* 五图网格 */
.metric-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
  flex: 1;
  min-height: 0;
}
.metric-cell {
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  padding: 6px 8px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.metric-cell-full { grid-column: 1 / -1; }
.metric-title { font-size: 10.5px; color: var(--text-3); margin-bottom: 2px; }
.metric-empty { font-size: 12px; padding: 16px 0; text-align: center; }
</style>
