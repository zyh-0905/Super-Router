<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../api'
import { store, toast } from '../store'
import RadarChart from '../components/RadarChart.vue'
import EmptyState from '../components/EmptyState.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import GroupSwitcher from '../components/GroupSwitcher.vue'
import SelectBox from '../components/SelectBox.vue'
import Icon from '../components/Icon.vue'
import { fmtDate, fmtTime, fmtScore, scoreWidth, downloadJSON } from '../utils'

const route = useRoute()

const loading = ref(true)
const decisions = ref([])
const selected = ref(null)

// ---- 编辑模式（多选删除 / 导出）----
const editMode = ref(false) // 进入编辑模式才显示复选框与批量操作
const checkedIds = ref([]) // 勾选的 request_id 列表
const showDeleteConfirm = ref(false)
const deleting = ref(false)

function toggleEdit() {
  editMode.value = !editMode.value
  if (!editMode.value) checkedIds.value = []
}

const allChecked = computed(() =>
  filtered.value.length > 0 && filtered.value.every(d => checkedIds.value.includes(d.request_id)))
const someChecked = computed(() =>
  checkedIds.value.length > 0 && !allChecked.value)

function toggleCheck(id) {
  const i = checkedIds.value.indexOf(id)
  if (i >= 0) checkedIds.value.splice(i, 1)
  else checkedIds.value.push(id)
}

function toggleAll() {
  if (allChecked.value) checkedIds.value = []
  else checkedIds.value = filtered.value.map(d => d.request_id)
}

async function confirmDelete() {
  deleting.value = true
  try {
    const r = await api.deleteDecisions(checkedIds.value)
    toast(`已删除 ${r.deleted ?? checkedIds.value.length} 条决策记录`, 'success')
    checkedIds.value = []
    showDeleteConfirm.value = false
    await load()
  } catch { /* api 层已提示 */ }
  finally { deleting.value = false }
}

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

// ---- 真实指标（直观展示，替代抽象 0-100 分）----
const roleLabel = r => ({ primary: '主力', backup: '备用', emergency: '应急' }[r] || r || '—')

function realValue(d, key) {
  const r = d?.raw || {}
  switch (key) {
    case 'cost': return r.cost_usd != null ? '$' + Number(r.cost_usd).toFixed(6) : '—'
    case 'reliability': return r.reliability != null ? (Number(r.reliability) * 100).toFixed(1) + '%' : '—'
    case 'latency': return r.ttft_ms != null ? r.ttft_ms + 'ms' : '无数据'
    case 'load': return r.recent_attempts != null ? r.recent_attempts + ' 次' : '—'
    case 'priority': return r.role ? roleLabel(r.role) + ' · ' + (r.user_priority ?? '—') : '—'
    case 'composite': return '第 ' + (rankOf(d) != null ? rankOf(d) : '—') + ' 名'
    default: return '—'
  }
}

// 该候选在候选集中的综合排名（按候选顺序）
function rankOf(d) {
  if (!d) return null
  const i = detailsList.value.findIndex(x => x.channel_id === d.channel_id)
  return i >= 0 ? i + 1 : null
}

// 人话排名总结："在 3 个候选中：费用第 1 低 · 首字节第 2 快 · 成功率第 1 高"
function rankSummary(d) {
  if (!d?.raw) return ''
  const list = detailsList.value
  const n = list.length
  if (n < 2) return `仅 ${n} 个候选通过筛选`
  const parts = []
  const rankOfKey = (key, asc) => {
    const vals = list.map(x => x.raw?.[key]).filter(v => v != null)
    if (vals.length < 2) return null
    const mine = d.raw?.[key]
    if (mine == null) return null
    const sorted = [...vals].sort((a, b) => (asc ? a - b : b - a))
    return sorted.indexOf(mine) + 1
  }
  const costRank = rankOfKey('cost_usd', true)
  const latRank = rankOfKey('ttft_ms', true)
  const relRank = rankOfKey('reliability', false)
  if (costRank) parts.push(`费用第 ${costRank} 低`)
  if (latRank) parts.push(`首字节第 ${latRank} 快`)
  if (relRank) parts.push(`成功率第 ${relRank} 高`)
  if (!parts.length) return ''
  return `在 ${n} 个候选中：${parts.join(' · ')}`
}

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
const strategyNames = {
  custom_priority: '手动优先级', price_first: '低价优先', latency_first: '低延迟优先',
  reliability_first: '高可靠优先', balanced: '加权均衡',
}

const modelOpts = computed(() => [{ value: '', label: '全部模型' }, ...models.value.map(m => ({ value: m, label: m }))])
const channelOpts = computed(() => [{ value: '', label: '全部渠道' }, ...channelNames.value.map(c => ({ value: c, label: c }))])
const strategyOpts = [{ value: '', label: '全部策略' }, ...strategies.map(s => ({ value: s, label: strategyNames[s] || s }))]

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

// 导出：勾选了条目则导出勾选的，否则导出当前筛选结果
function exportSelection() {
  const list = checkedIds.value.length
    ? filtered.value.filter(d => checkedIds.value.includes(d.request_id))
    : filtered.value
  downloadJSON('decisions.json', list)
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
        <button class="btn btn-ghost" @click="toggleEdit">
          <Icon :name="editMode ? 'check' : 'pencil'" :size="15" />{{ editMode ? '完成' : '编辑' }}
        </button>
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
            <SelectBox v-model="modelFilter" :options="modelOpts" width="132px" />
            <SelectBox v-model="channelFilter" :options="channelOpts" width="132px" />
            <SelectBox v-model="strategyFilter" :options="strategyOpts" width="150px" />
          </div>
          <!-- 编辑模式工具栏：多选 + 导出 + 删除 -->
          <div v-if="editMode && filtered.length" class="row gap-2 mt-2" style="align-items:center">
            <input type="checkbox" class="dec-check" :checked="allChecked" :indeterminate="someChecked" @change="toggleAll" aria-label="全选当前筛选结果" />
            <span class="text-3" style="font-size:12px">已选 {{ checkedIds.length }} 项</span>
            <span class="spacer" />
            <button class="btn btn-ghost btn-sm" @click="exportSelection"><Icon name="download" :size="13" />导出 JSON</button>
            <button class="btn btn-danger btn-sm" :disabled="!checkedIds.length" @click="showDeleteConfirm = true">
              <Icon name="trash" :size="13" />删除选中
            </button>
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
              <input v-if="editMode" type="checkbox" class="dec-check" :checked="checkedIds.includes(d.request_id)" @change="toggleCheck(d.request_id)" @click.stop aria-label="选择此条决策" />
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
              <!-- 人话排名总结 -->
              <div v-if="rankSummary(activeDetail)" class="summary-line">
                <Icon name="check" :size="13" style="color:var(--green)" />
                <span>{{ rankSummary(activeDetail) }}</span>
              </div>
              <div v-if="!activeDetail?.raw" class="text-3" style="font-size:11px">该记录来自旧版本，仅有抽象评分（无真实指标）</div>
              <!-- 真实指标芯片（真实值为主，抽象分弱化为小字） -->
              <div class="dim-chips">
                <div v-for="dl in dimMeta" :key="dl.key" class="dim-chip"
                  :style="{ background: hexToRgba(dl.color, 0.06 + dimVal(activeDetail, dl.key) / 100 * 0.5), borderColor: hexToRgba(dl.color, 0.55) }">
                  <span class="dim-chip-label">{{ dl.label }}</span>
                  <span class="dim-chip-val" :style="{ color: dl.color }">{{ realValue(activeDetail, dl.key) }}</span>
                  <span class="dim-chip-score mono" :title="'抽象评分（0-100）：' + dimVal(activeDetail, dl.key).toFixed(1)">{{ dimVal(activeDetail, dl.key).toFixed(0) }}分</span>
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
    <!-- 删除确认对话框 -->
    <ConfirmDialog
      v-if="showDeleteConfirm"
      title="删除决策记录"
      :message="`确定要删除选中的 ${checkedIds.length} 条决策记录吗？此操作不可撤销。`"
      confirm-text="删除"
      cancel-text="取消"
      danger
      @confirm="confirmDelete"
      @cancel="showDeleteConfirm = false"
    />
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
.dec-check { width: 15px; height: 15px; accent-color: var(--blue); cursor: pointer; flex-shrink: 0; margin: 0; }
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
.summary-line {
  display: flex; align-items: center; gap: 6px;
  padding: 7px 11px;
  background: rgba(48, 209, 88, 0.08);
  border: 1px solid rgba(48, 209, 88, 0.25);
  border-radius: var(--radius-md);
  font-size: 12px; font-weight: 600; color: var(--text-1);
}
.dim-chips {
  display: grid; grid-template-columns: repeat(3, 1fr); gap: 6px;
}
.dim-chip {
  display: flex; flex-direction: column; gap: 2px;
  padding: 8px 11px; border-radius: var(--radius-md);
  border: 1px solid transparent;
  transition: transform var(--dur) var(--ease);
}
.dim-chip:hover { transform: translateY(-1px); }
.dim-chip-label { font-size: 10.5px; color: var(--text-2); }
.dim-chip-val { font-size: 13.5px; font-weight: 700; font-variant-numeric: tabular-nums; }
.dim-chip-score { font-size: 9.5px; color: var(--text-3); }

@media (max-width: 1000px) {
  .dec-layout { grid-template-columns: 1fr; }
  .dec-list { max-height: 320px; }
  .dim-chips { grid-template-columns: repeat(3, 1fr); }
}
</style>
