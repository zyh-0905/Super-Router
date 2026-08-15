<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../api'
import { store } from '../store'
import RadarChart from '../components/RadarChart.vue'
import EmptyState from '../components/EmptyState.vue'
import GroupSwitcher from '../components/GroupSwitcher.vue'
import Icon from '../components/Icon.vue'
import { fmtDate, fmtTime, fmtScore, scoreWidth, downloadJSON } from '../utils'

const route = useRoute()

const loading = ref(true)
const decisions = ref([])
const selected = ref(null)

// ---- 六维评分雷达图（美化版）----
const dimMeta = [
  { key: 'cost', label: '成本', color: '#30d158' },
  { key: 'reliability', label: '可靠性', color: '#0a84ff' },
  { key: 'latency', label: '延迟', color: '#ff9f0a' },
  { key: 'load', label: '负载', color: '#bf5af2' },
  { key: 'priority', label: '优先级', color: '#ff375f' },
  { key: 'composite', label: '综合得分', color: '#64d2ff' },
]
const radarPalette = ['#0a84ff', '#30d158', '#ff9f0a', '#bf5af2', '#ff375f', '#64d2ff', '#ffd60a', '#ac8e68']

function hexToRgba(hex, alpha) {
  const r = parseInt(hex.slice(1, 3), 16)
  const g = parseInt(hex.slice(3, 5), 16)
  const b = parseInt(hex.slice(5, 7), 16)
  return `rgba(${r}, ${g}, ${b}, ${alpha})`
}

const dimVal = (d, key) => Number(d?.dims?.[key] ?? 50)

const detailsList = computed(() => (selected.value?.candidate_details || []).filter(d => d.dims))
const hoveredIdx = ref(null)
watch(selected, () => { hoveredIdx.value = null })

const activeIdx = computed(() =>
  hoveredIdx.value != null && hoveredIdx.value < detailsList.value.length ? hoveredIdx.value : 0)
const activeDetail = computed(() => detailsList.value[activeIdx.value] || null)
const activeColor = computed(() => radarPalette[activeIdx.value % radarPalette.length])
const isSelectedDetail = computed(() => activeIdx.value === 0)

// ---- 筛选 ----
const search = ref('')
const modelFilter = ref('')
const channelFilter = ref('')
const strategyFilter = ref('')

const models = computed(() => [...new Set(decisions.value.map(d => d.model).filter(Boolean))])
const channelNames = computed(() => [...new Set(decisions.value.map(d => d.selected_channel).filter(Boolean))])
const strategies = ['custom_priority', 'price_first', 'latency_first', 'reliability_first', 'balanced']

const filtered = computed(() =>
  decisions.value.filter(d => {
    if (search.value && !d.request_id?.includes(search.value)) return false
    if (modelFilter.value && d.model !== modelFilter.value) return false
    if (channelFilter.value && d.selected_channel !== channelFilter.value) return false
    if (strategyFilter.value && (d.strategy || d.policy_version) !== strategyFilter.value) return false
    return true
  })
)

async function load() {
  loading.value = true
  try {
    const r = await api.decisions(100, store.currentGroup)
    decisions.value = r.decisions || []
    // 深链接：/decisions?id=xxx 自动选中
    const id = route.query.id
    if (id) {
      const d = decisions.value.find(x => x.request_id === id)
      if (d) selected.value = d
    } else if (!selected.value || !decisions.value.some(x => x.request_id === selected.value.request_id)) {
      selected.value = decisions.value[0] || null
    }
  } catch { /* 已提示 */ }
  finally { loading.value = false }
}

function onGroupChange() { load() }

const exclusionLabel = r => ({
  user_disabled: '已禁用', model_not_supported: '不支持该模型', capability_missing: '缺少能力',
  credential_invalid: '凭证失效', quota_exhausted: '配额耗尽', over_price_cap: '超出价格上限',
  circuit_open: '熔断开启', circuit_cooling: '熔断冷却中', circuit_half_open: '熔断半开',
  latency_cap_exceeded: '延迟超限', region_mismatch: '区域不符', protocol_unsupported: '协议不支持',
  group_not_allowed: '不在允许分组', 
}[r] || r)

function exportAll() {
  downloadJSON('decisions.json', filtered.value)
}

onMounted(load)
watch(() => route.query.id, load)
</script>

<template>
  <div class="page-wrap fade-in">
    <!-- 页头 -->
    <div class="page-head">
      <div>
        <div class="page-title">决策</div>
        <div class="page-sub">审计每一次路由决策：左侧滚动浏览，右侧六维评分对比</div>
      </div>
      <div class="row gap-2">
        <GroupSwitcher @change="onGroupChange" />
        <button class="btn btn-ghost" @click="load" :disabled="loading"><Icon name="refresh" :size="15" />刷新</button>
        <button class="btn btn-ghost" @click="exportAll"><Icon name="download" :size="15" />导出 JSON</button>
      </div>
    </div>

    <div class="dec-layout">
      <!-- 左：筛选 + 滚动列表 -->
      <div class="dec-left">
        <div class="card" style="padding:12px 14px">
          <div class="row gap-2" style="flex-wrap:wrap">
            <div class="grow" style="position:relative;min-width:140px">
              <span style="position:absolute;left:11px;top:50%;transform:translateY(-50%);color:var(--text-3);display:flex"><Icon name="search" :size="14" /></span>
              <input v-model="search" class="input mono" placeholder="搜索 Request ID" style="padding-left:33px">
            </div>
            <select v-model="modelFilter" class="select" style="width:126px"><option value="">全部模型</option><option v-for="m in models" :key="m" :value="m">{{ m }}</option></select>
            <select v-model="channelFilter" class="select" style="width:126px"><option value="">全部渠道</option><option v-for="c in channelNames" :key="c" :value="c">{{ c }}</option></select>
            <select v-model="strategyFilter" class="select" style="width:150px"><option value="">全部策略</option><option v-for="s in strategies" :key="s" :value="s">{{ s }}</option></select>
          </div>
        </div>

        <div class="dec-list">
          <div v-if="loading" class="col" style="gap:8px">
            <div v-for="i in 6" :key="i" class="card skeleton" style="height:64px" />
          </div>
          <EmptyState v-else-if="filtered.length === 0" icon="list" title="暂无决策记录" desc="在「测试台」发送请求后，路由决策会出现在这里" style="padding:40px 0" />
          <div
            v-for="d in filtered" :key="d.request_id"
            class="card dec-row" :class="{ selected: selected?.request_id === d.request_id }"
            @click="selected = d"
          >
            <div class="row gap-2">
              <span class="mono text-3 nowrap" style="font-size:11px">{{ fmtTime(d.decided_at) }}</span>
              <span class="badge badge-blue">{{ d.model }}</span>
              <span class="grow truncate" style="font-weight:600;font-size:13px">{{ d.selected_channel || '—' }}</span>
            </div>
            <div class="row gap-2 mt-1">
              <span class="badge badge-purple" style="font-size:10px">{{ d.strategy || d.policy_version || '—' }}</span>
              <span v-if="d.group_name" class="badge badge-teal" style="font-size:10px">{{ d.group_name }}</span>
              <span class="text-3 mono" style="font-size:11px;margin-left:auto">候选 {{ d.candidate_order?.length ?? 0 }}</span>
              <span class="mono" :style="{ color: (d.excluded || []).length ? 'var(--red)' : 'var(--text-3)', fontSize: '11px' }">排除 {{ (d.excluded || []).length }}</span>
            </div>
            <div class="mono text-3 truncate" style="font-size:10.5px;margin-top:3px" :title="d.request_id">{{ d.request_id }}</div>
          </div>
        </div>
      </div>

      <!-- 右：大六维雷达 + 详情 -->
      <div class="dec-right">
        <EmptyState v-if="!selected" icon="gauge" title="选择一个决策" desc="点击左侧任意决策查看六维评分对比" style="margin:auto" />
        <template v-else>
          <div class="card card-pad mb-3" style="padding:12px 18px">
            <div class="row gap-2" style="align-items:center;flex-wrap:wrap">
              <span style="font-size:15px;font-weight:700">六维评分对比</span>
              <span class="badge badge-blue">{{ selected.model }}</span>
              <span class="badge badge-purple">{{ selected.strategy || selected.policy_version || '—' }}</span>
              <span v-if="selected.group_name" class="badge badge-teal">{{ selected.group_name }}</span>
              <span class="text-3" style="font-size:12px;margin-left:auto">选中：<b style="color:var(--text-1)">{{ selected.selected_channel || '—' }}</b> · {{ fmtDate(selected.decided_at) }}</span>
            </div>
          </div>

          <div v-if="detailsList.length" class="card card-pad mb-3" style="padding:14px 18px">
            <div class="field-label mb-1">六维评分（0-100 越大越好，无数据取中性 50 · 光晕高亮为选中渠道）</div>
            <RadarChart :details="detailsList" :dims="dimMeta" @hover="hoveredIdx = $event" />
            <div v-if="activeDetail" class="dim-strip">
              <div class="row gap-2" style="align-items:center">
                <span class="dot" style="width:9px;height:9px" :style="{ background: activeColor, boxShadow: '0 0 8px ' + hexToRgba(activeColor, 0.9) }" />
                <span style="font-weight:700;font-size:12.5px">{{ activeDetail.channel || ('#' + activeDetail.channel_id) }}</span>
                <span class="badge" :class="isSelectedDetail ? 'badge-green' : 'badge-gray'">{{ isSelectedDetail ? '已选渠道' : '悬停预览' }}</span>
              </div>
              <div class="dim-chips">
                <div v-for="dl in dimMeta" :key="dl.key" class="dim-chip"
                  :style="{ background: hexToRgba(dl.color, 0.06 + dimVal(activeDetail, dl.key) / 100 * 0.5), borderColor: hexToRgba(dl.color, 0.55) }">
                  <span class="dim-chip-label">{{ dl.label }}</span>
                  <span class="dim-chip-val" :style="{ color: dl.color }">{{ dimVal(activeDetail, dl.key).toFixed(1) }}</span>
                </div>
              </div>
            </div>
          </div>
          <div v-else class="card card-pad mb-3 text-3" style="padding:16px 18px;font-size:12.5px">
            该决策没有候选评分数据（旧记录或候选为空），下方展示原始候选排序。
          </div>

          <div class="card card-pad">
            <div class="field">
              <label class="field-label">决策原因</label>
              <div class="code">{{ selected.decision_reason || '—' }}</div>
            </div>

            <div v-if="(selected.candidate_order || []).length" class="field">
              <label class="field-label">候选排序（得分）</label>
              <div v-for="(c, i) in selected.candidate_order" :key="i" class="row gap-3 cand-row">
                <span class="mono text-3" style="width:18px">{{ i + 1 }}</span>
                <span class="grow truncate" style="font-size:13px">{{ c.channel }}</span>
                <div class="score-track"><div class="score-fill" :style="{ width: scoreWidth(c.score, selected.candidate_order) + '%' }" /></div>
                <span class="mono text-3" style="width:56px;text-align:right;font-size:11px">{{ fmtScore(c.score) }}</span>
                <span v-if="i === 0" class="badge badge-green" style="width:52px;justify-content:center">已选</span>
                <span v-else style="width:52px" />
              </div>
            </div>

            <div v-if="(selected.excluded || []).length" class="field">
              <label class="field-label">排除站点</label>
              <div v-for="(e, i) in selected.excluded" :key="i" class="row gap-2 excl-row">
                <span class="text-3" style="font-size:12.5px;width:130px;flex-shrink:0">{{ e.channel || '—' }}</span>
                <span class="badge badge-red">{{ exclusionLabel(e.reason) }}</span>
              </div>
            </div>

            <div class="row gap-2 mt-2" style="justify-content:flex-end">
              <button class="btn btn-ghost btn-sm" @click="navigator.clipboard.writeText(JSON.stringify(selected, null, 2))"><Icon name="copy" :size="13" />复制 JSON</button>
            </div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dec-layout { display: grid; grid-template-columns: 400px 1fr; gap: 16px; align-items: start; }
.dec-left { display: flex; flex-direction: column; gap: 12px; }
.dec-list {
  display: flex; flex-direction: column; gap: 8px;
  max-height: calc(100vh - 250px);
  overflow-y: auto; padding-right: 2px;
}
.dec-row { padding: 10px 14px; cursor: pointer; transition: all var(--dur) var(--ease); }
.dec-row:hover { transform: translateY(-1px); box-shadow: var(--shadow-raised); }
.dec-row.selected { border-color: var(--blue); box-shadow: 0 0 0 3.5px var(--blue-soft); }

.dec-right { display: flex; flex-direction: column; min-height: 560px; }

.cand-row { padding: 6px 0; }
.excl-row { padding: 4px 0; }
.score-track { width: 130px; height: 5px; background: var(--border); border-radius: 3px; overflow: hidden; flex-shrink: 0; }
.score-fill { height: 100%; background: var(--blue); border-radius: 3px; transition: width 0.4s ease; }

/* 六维芯片条 */
.dim-strip {
  margin-top: 10px; padding-top: 10px;
  border-top: 1px dashed var(--border);
  display: flex; flex-direction: column; gap: 8px;
}
.dim-chips {
  display: grid; grid-template-columns: repeat(6, 1fr); gap: 6px;
}
.dim-chip {
  display: flex; align-items: center; justify-content: space-between; gap: 4px;
  padding: 6px 9px; border-radius: var(--radius-md);
  border: 1px solid transparent;
  transition: transform var(--dur) var(--ease);
}
.dim-chip:hover { transform: translateY(-1px); }
.dim-chip-label { font-size: 10.5px; color: var(--text-2); }
.dim-chip-val { font-size: 13px; font-weight: 700; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }

@media (max-width: 1000px) {
  .dec-layout { grid-template-columns: 1fr; }
  .dec-list { max-height: 320px; }
  .dim-chips { grid-template-columns: repeat(3, 1fr); }
}
</style>
