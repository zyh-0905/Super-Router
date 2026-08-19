<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { api } from '../api'
import { store, toast } from '../store'
import BaseModal from '../components/BaseModal.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import BaseChart from '../components/BaseChart.vue'
import Icon from '../components/Icon.vue'
import SelectBox from '../components/SelectBox.vue'

// ---- 下拉选项（统一 SelectBox 组件）----
const filterOpts = [
  { value: '', label: '全部' },
  { value: 'closed', label: '正常' },
  { value: 'open', label: '熔断' },
  { value: 'disabled', label: '禁用' },
]
const relayTypeOpts = [
  { value: 'custom', label: '自定义' },
  { value: 'newapi', label: 'New API / One API 系' },
  { value: 'sub2api', label: 'Sub2API' },
]
const protocolOpts = [
  { value: 'openai', label: 'OpenAI 兼容' },
  { value: 'anthropic', label: 'Claude 原生' },
]
const roleOpts = [
  { value: 'primary', label: '主力' },
  { value: 'backup', label: '备用' },
  { value: 'emergency', label: '应急' },
]
import { fmtDate, fmtTime, fmtNum, fmtMs, fmtPct } from '../utils'

const route = useRoute()

const loading = ref(true)
const channels = ref([])
const circuitStates = ref([])
const selected = ref(null)
const detailTab = ref('info')
const health = ref([])
const healthLoading = ref(false)

// 搜索/筛选
const search = ref('')
const filter = ref('')

// 新增/编辑弹窗
const showModal = ref(false)
const editing = ref(null)
const saving = ref(false)
const form = ref(emptyForm())
const testResult = ref(null)
const testing = ref(false)
// 乐观锁：打开编辑弹窗时记录的 updated_at；余额令牌脱敏状态（后端不再返回明文）
const editExpectedUpdatedAt = ref(null)
const balanceTokenStatus = ref(null) // null=新建 | { configured: boolean, suffix: string }

// 上游模型列表弹窗
const showModels = ref(false)
const modelsLoading = ref(false)
const modelsError = ref(null)
const upstreamModels = ref([])
const modelsSource = ref('detail') // 'detail' = 已保存站点（服务端凭据）| 'form' = 表单值

// 分组
const groups = ref([])
const groupFilter = ref(null) // null = 全部
const showGroupModal = ref(false)
const editingGroup = ref(null)
const savingGroup = ref(false)
const groupForm = ref(emptyGroupForm())

function emptyGroupForm() {
  return {
    name: '', description: '', default_strategy: '', group_priority: 0,
    alive_interval_seconds: 0, pricing_interval_seconds: 0, probe_interval_seconds: 0,
    balance_interval_seconds: 0,
    daily_probe_budget: 0,
    cb_min_samples: 0, cb_open_failure_rate: 0, cb_open_min_failures: 0,
    cb_initial_cooling_seconds: 0, cb_max_cooling_seconds: 0,
  }
}

// 策略 id → 中文名（分组列表徽标展示）
const strategyNames = {
  custom_priority: '手动优先级', price_first: '低价优先', latency_first: '低延迟优先',
  reliability_first: '高可靠优先', balanced: '加权均衡',
}
function strategyLabel(id) { return strategyNames[id] || id }

async function loadGroups() {
  try {
    const r = await api.listGroups()
    groups.value = r.groups || []
    store.groups = groups.value
  } catch { /* 已提示 */ }
}

function openGroupModal(g) {
  editingGroup.value = g || null
  groupForm.value = g
    ? {
        name: g.name, description: g.description || '', default_strategy: g.default_strategy || '',
        group_priority: g.group_priority || 0,
        alive_interval_seconds: g.alive_interval_seconds || 0,
        pricing_interval_seconds: g.pricing_interval_seconds || 0,
        probe_interval_seconds: g.probe_interval_seconds || 0,
        balance_interval_seconds: g.balance_interval_seconds || 0,
        daily_probe_budget: g.daily_probe_budget || 0,
        cb_min_samples: g.cb_min_samples || 0, cb_open_failure_rate: g.cb_open_failure_rate || 0,
        cb_open_min_failures: g.cb_open_min_failures || 0,
        cb_initial_cooling_seconds: g.cb_initial_cooling_seconds || 0,
        cb_max_cooling_seconds: g.cb_max_cooling_seconds || 0,
      }
    : emptyGroupForm()
  showGroupModal.value = true
}

async function saveGroup() {
  if (!groupForm.value.name) { toast('请填写分组名称', 'error'); return }
  savingGroup.value = true
  try {
    const p = { ...groupForm.value }
    // 路由策略统一在「策略中心」配置，此弹窗不再提交策略字段（避免覆盖）
    delete p.default_strategy
    // 数字字段归一
    for (const k of ['group_priority', 'alive_interval_seconds', 'pricing_interval_seconds', 'probe_interval_seconds', 'balance_interval_seconds', 'cb_min_samples', 'cb_open_min_failures', 'cb_initial_cooling_seconds', 'cb_max_cooling_seconds']) {
      p[k] = Number(p[k]) || 0
    }
    p.daily_probe_budget = Number(p.daily_probe_budget) || 0
    p.cb_open_failure_rate = Number(p.cb_open_failure_rate) || 0
    if (editingGroup.value) await api.updateGroup(editingGroup.value.id, p)
    else await api.createGroup(p)
    toast(editingGroup.value ? '分组已更新' : '分组已创建', 'success')
    showGroupModal.value = false
    await loadGroups()
  } catch { /* 已提示 */ }
  finally { savingGroup.value = false }
}

// 删除分组确认
const confirmDeleteGroup = ref(null)

function askDeleteGroup(g) {
  confirmDeleteGroup.value = g
}

async function doDeleteGroup() {
  const g = confirmDeleteGroup.value
  confirmDeleteGroup.value = null
  try {
    await api.deleteGroup(g.id)
    toast('分组已删除', 'success')
    if (groupFilter.value === g.id) groupFilter.value = null
    if (store.currentGroup === g.id) store.currentGroup = null
    await loadGroups()
  } catch { /* 已提示 */ }
}

function emptyForm() {
  return {
    name: '', base_url: '', access_token: '', api_key: '',
    protocol: 'openai',
    relay_type: 'custom',
    role: 'primary', user_priority: 100, weight: 1,
    daily_probe_budget: 1.0,
    balance_api_url: '',
    balance_api_token: '',
    model_pairs: [{ from: 'gpt-4o', to: 'gpt-4o' }],
    capabilities: ['tools'],
    group_ids: [],
  }
}

// 中转站类型 → 默认余额接口路径
const relayTypeDefaults = {
  newapi: '/api/user/self',
  sub2api: '/api/v1/auth/me',
  custom: '',
}
// 用户在表单里手动切换中转站类型时，自动填入对应余额接口地址（可手动再改）
function onRelayTypeChange() {
  const ep = relayTypeDefaults[form.value.relay_type]
  if (ep) form.value.balance_api_url = ep
}

const filtered = computed(() =>
  channels.value.filter(c => {
    if (search.value && !c.name.toLowerCase().includes(search.value.toLowerCase())) return false
    if (groupFilter.value != null && !(c.groups || []).some(g => g.id === groupFilter.value)) return false
    if (filter.value === 'open' && c.circuit_state !== 'open') return false
    if (filter.value === 'closed' && c.circuit_state && c.circuit_state !== 'closed') return false
    if (filter.value === 'disabled' && c.enabled) return false
    return true
  })
)

const circuitOf = id => circuitStates.value.filter(s => s.channel_id === id)

function dotClass(ch) {
  if (!ch.enabled) return 'dot dot-gray'
  if (ch.circuit_state === 'open') return 'dot dot-red dot-pulse'
  if (ch.circuit_state === 'half_open') return 'dot dot-orange'
  if (ch.circuit_state === 'degraded') return 'dot dot-orange dot-pulse'
  return 'dot dot-green'
}

function circuitBadge(state) {
  const map = { closed: 'badge-green', half_open: 'badge-orange', degraded: 'badge-orange', open: 'badge-red' }
  const label = { closed: '正常', half_open: '半开', degraded: '降级', open: '熔断' }
  return { cls: map[state] || 'badge-gray', label: label[state] || state || '正常' }
}

function roleBadge(role) {
  return { primary: 'badge-blue', backup: 'badge-purple', emergency: 'badge-red' }[role] || 'badge-gray'
}
// 协议/类型/角色的展示标签（OpenAI/Anthropic 首字母大写、类型与角色中文标签）
function protocolLabel(p) { return p === 'anthropic' ? 'Anthropic' : 'OpenAI' }
function protocolBadge(p) { return p === 'anthropic' ? 'badge-purple' : 'badge-blue' }
function relayTypeLabel(t) { return { newapi: 'New API', sub2api: 'Sub2API', custom: '自定义' }[t] || t || '' }
function roleLabelCn(r) { return { primary: '主力', backup: '备用', emergency: '应急' }[r] || r || '' }

function relayTypeBadge(t) {
  if (!t || t === 'custom') return null
  return { newapi: 'badge-teal', sub2api: 'badge-blue' }[t] || 'badge-gray'
}

async function load() {
  loading.value = true
  try {
    const [ch, ci, st] = await Promise.all([api.listChannels(), api.circuit(), api.stats()])
    const chStats = st.channels || []
    const circuitList = ci.states || []
    channels.value = (ch.channels || []).map(c => {
      const cs = chStats.find(s => s.id === c.id) || {}
      const worst = circuitList.filter(s => s.channel_id === c.id).sort((a, b) => rank(b.state) - rank(a.state))[0]
      return {
        ...c,
        requests_24h: cs.requests_24h ?? null,
        success_rate: cs.success_rate ?? null,
        avg_latency_ms: cs.avg_latency_ms ?? null,
        balance: cs.balance ?? null,
        balance_currency: cs.balance_currency ?? 'USD',
        balance_source: cs.balance_source ?? null,
        balance_error: cs.balance_error ?? null,
        balance_checked_at: cs.balance_checked_at ?? null,
        circuit_state: worst ? worst.state : null,
      }
    })
    circuitStates.value = circuitList
    store.channels = channels.value
    if (selected.value) {
      selected.value = channels.value.find(c => c.id === selected.value.id) || null
    }
    // 深链接：?select=站点ID 自动选中并打开详情
    const sel = Number(route.query.select)
    if (sel && !selected.value) {
      const target = channels.value.find(c => c.id === sel)
      if (target) select(target)
    }
  } catch { /* api 层已提示 */ }
  finally { loading.value = false }
}

function rank(s) { return { open: 3, degraded: 2, half_open: 1, closed: 0 }[s] ?? 0 }

function select(ch) {
  selected.value = ch
  detailTab.value = 'info'
  health.value = []
  balance.value = null
  ratio.value = null
  probeResult.value = null
  showRatioGroupModal.value = false
  ratioLimitInput.value = ch.ratio_limit || 0
}

async function loadHealth() {
  if (!selected.value) return
  const id = selected.value.id
  healthLoading.value = true
  try {
    const r = await api.channelHealth(id)
    if (selected.value?.id !== id) return // 站点已切换，丢弃过期响应
    health.value = r.health_checks || []
  } catch { health.value = [] }
  finally { if (selected.value?.id === id) healthLoading.value = false }
}

// ===== 余额 =====
const balance = ref(null) // {latest, history}
const balanceLoading = ref(false)

async function loadBalance() {
  if (!selected.value) return
  const id = selected.value.id
  balanceLoading.value = true
  try {
    const r = await api.channelBalance(id)
    if (selected.value?.id !== id) return // 站点已切换，丢弃过期响应
    balance.value = r
  } catch { balance.value = null }
  finally { if (selected.value?.id === id) balanceLoading.value = false }
}

// ===== 倍率 =====
const ratio = ref(null) // {declared, history, latest}
const ratioLoading = ref(false)
const probing = ref(false)
const probeModel = ref('')
const probeModelOpts = computed(() => modelKeys.value.map(m => ({ value: m, label: m })))
const probeTokens = ref(64)
const probeResult = ref(null) // 最近一次实测结果
const modelPrices = ref([]) // 官方模型价格库
const declaredInPrice = ref(null) // 所选模型未收录时用户声明的官网输入价
const declaredOutPrice = ref(null)

const currentModelPrice = computed(() => modelPrices.value.find(p => p.model === probeModel.value) || null)
const priceMissing = computed(() => !!probeModel.value && !currentModelPrice.value)

// 倍率上限（站点级，0 = 不限），在倍率页签内直接设置
const ratioLimitInput = ref(0)
const savingRatioLimit = ref(false)

const overLimitNow = computed(() => {
  if (ratioLimitInput.value <= 0) return false
  const latest = ratio.value?.latest || {}
  return Object.values(latest).some(l => Number(l.real_ratio) > Number(ratioLimitInput.value))
})

async function saveRatioLimit() {
  if (!selected.value) return
  savingRatioLimit.value = true
  try {
    await api.updateChannel(selected.value.id, { ratio_limit: Number(ratioLimitInput.value) || 0 })
    toast(ratioLimitInput.value > 0 ? `倍率上限已设为 ${Number(ratioLimitInput.value).toFixed(4)}x` : '已取消倍率上限限制', 'success')
    await load()
  } catch { /* api 层已提示 */ }
  finally { savingRatioLimit.value = false }
}

async function loadRatio() {
  if (!selected.value) return
  const id = selected.value.id
  ratioLoading.value = true
  try {
    const [r, mp] = await Promise.all([api.channelRatio(id), api.listModelPrices()])
    if (selected.value?.id !== id) return // 站点已切换，丢弃过期响应
    ratio.value = r
    modelPrices.value = mp.prices || []
    if (!probeModel.value) {
      const keys = Object.keys(selected.value.model_mapping || {})
      probeModel.value = keys[0] || ''
    }
  } catch { ratio.value = null }
  finally { if (selected.value?.id === id) ratioLoading.value = false }
}

async function runProbe() {
  if (!selected.value || probing.value) return
  if (!probeModel.value) { toast('请先选择要实测的模型', 'error'); return }
  // 价格库未收录：需要用户先声明官网价
  let official = null
  if (priceMissing.value) {
    const inP = Number(declaredInPrice.value)
    const outP = Number(declaredOutPrice.value)
    if (!(inP > 0) || !(outP > 0)) { toast('该模型暂无官方价格，请先声明官网输入/输出价（$/1M）', 'error'); return }
    official = { input: inP, output: outP }
  }
  probing.value = true
  probeResult.value = null
  try {
    probeResult.value = await api.probeRatio(selected.value.id, probeModel.value, Number(probeTokens.value) || 64, official)
    toast('实测完成，结果已写入路由数据', 'success')
    await Promise.all([loadRatio(), load()])
  } catch { /* api 层已提示（含 400/409/429/502 原因） */ }
  finally { probing.value = false }
}

const modelKeys = computed(() => Object.keys(selected.value?.model_mapping || {}))

const ratioChartOption = computed(() => {
  const hist = [...(ratio.value?.history || [])].filter(h => h.model === probeModel.value).reverse()
  return {
    grid: { left: 50, right: 14, top: 20, bottom: 26 },
    xAxis: { type: 'category', data: hist.map(h => h.checked_at.slice(5, 16).replace('T', ' ')), axisLabel: { fontSize: 10, interval: Math.max(0, Math.floor(hist.length / 8)) } },
    yAxis: { type: 'value', axisLabel: { formatter: '{value}x' } },
    tooltip: {
      trigger: 'axis',
      formatter: ps => {
        const p = Array.isArray(ps) ? ps[0] : ps
        const idx = p?.dataIndex
        const h = idx != null ? hist[idx] : null
        if (!h) return ''
        return `${h.checked_at.slice(5, 16).replace('T', ' ')}<br/>实测倍率: <b>${Number(h.real_ratio).toFixed(4)}x</b><br/>来源: ${h.source === 'manual' ? '手动实测' : '定时探针'}<br/>tokens: ${h.tokens_used} · ttft: ${h.ttft_ms}ms`
      },
    },
    series: [{
      type: 'line', smooth: true, symbol: 'circle', symbolSize: 5,
      data: hist.map(h => h.real_ratio),
      lineStyle: { color: '#0a84ff', width: 2 },
      areaStyle: { color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [{ offset: 0, color: 'rgba(10,132,255,0.14)' }, { offset: 1, color: 'rgba(10,132,255,0)' }] } },
    }],
  }
})

function fmtDrift(v) {
  if (v == null) return null
  return (v > 0 ? '+' : '') + v.toFixed(1) + '%'
}

// ===== 倍率检测分组 =====
const showRatioGroupModal = ref(false)
const editingRatioGroup = ref(null)
const savingRatioGroup = ref(false)
const ratioGroupForm = ref({ name: '', models: [], default_model: '' })
const ratioDefaultModelOpts = computed(() => (ratioGroupForm.value.models || []).map(m => ({ value: m, label: m })))
const probingGroupId = ref(null)
const probingAll = ref(false)

function openRatioGroupModal(g) {
  editingRatioGroup.value = g || null
  ratioGroupForm.value = g
    ? { name: g.name, models: [...(g.models || [])], default_model: g.default_model || '' }
    : { name: '', models: [], default_model: '' }
  showRatioGroupModal.value = true
}

function toggleGroupModel(m) {
  const i = ratioGroupForm.value.models.indexOf(m)
  if (i >= 0) {
    ratioGroupForm.value.models.splice(i, 1)
    if (ratioGroupForm.value.default_model === m) ratioGroupForm.value.default_model = ''
  } else {
    ratioGroupForm.value.models.push(m)
    if (!ratioGroupForm.value.default_model) ratioGroupForm.value.default_model = m
  }
}

async function saveRatioGroup() {
  if (!selected.value) return
  const f = ratioGroupForm.value
  if (!f.name) { toast('请填写分组名称', 'error'); return }
  if (!f.models.length) { toast('请至少选择一个模型', 'error'); return }
  if (!f.default_model) { toast('请选择默认检测模型', 'error'); return }
  savingRatioGroup.value = true
  try {
    if (editingRatioGroup.value) await api.updateRatioGroup(selected.value.id, editingRatioGroup.value.id, f)
    else await api.createRatioGroup(selected.value.id, f)
    toast(editingRatioGroup.value ? '分组已更新' : '分组已创建', 'success')
    showRatioGroupModal.value = false
    await loadRatio()
  } catch { /* api 层已提示 */ }
  finally { savingRatioGroup.value = false }
}

// 删除倍率分组确认
const confirmDeleteRatioGroup = ref(null)

function askDeleteRatioGroup(g) {
  confirmDeleteRatioGroup.value = g
}

async function doDeleteRatioGroup() {
  const g = confirmDeleteRatioGroup.value
  confirmDeleteRatioGroup.value = null
  if (!selected.value) return
  try {
    await api.deleteRatioGroup(selected.value.id, g.id)
    toast('分组已删除', 'success')
    await loadRatio()
  } catch { /* api 层已提示 */ }
}

async function probeGroup(g) {
  if (!selected.value || probingGroupId.value || probingAll.value) return
  probingGroupId.value = g.id
  probeResult.value = null
  try {
    probeResult.value = await api.probeRatioGroup(selected.value.id, g.id)
    toast(`分组「${g.name}」实测完成，代表倍率已更新`, 'success')
    await Promise.all([loadRatio(), load()])
  } catch { /* api 层已提示 */ }
  finally { probingGroupId.value = null }
}

async function probeAllGroups() {
  if (!selected.value || probingAll.value || probingGroupId.value) return
  const groups = ratio.value?.groups || []
  if (!groups.length) return
  probingAll.value = true
  let ok = 0, fail = 0
  try {
    for (const g of groups) {
      try {
        const r = await api.probeRatioGroup(selected.value.id, g.id)
        probeResult.value = r // 展示最近一组的实测结果
        ok++
      } catch { fail++ }
    }
    toast(`一键实测完成：成功 ${ok} 组${fail ? `，失败 ${fail} 组` : ''}`, fail ? 'info' : 'success')
    await Promise.all([loadRatio(), load()])
  } finally { probingAll.value = false }
}

const balanceChartOption = computed(() => {
  const hist = [...(balance.value?.history || [])].reverse()
  return {
    grid: { left: 50, right: 14, top: 20, bottom: 26 },
    xAxis: { type: 'category', data: hist.map(h => h.checked_at.slice(5, 16).replace('T', ' ')), axisLabel: { fontSize: 10, interval: Math.max(0, Math.floor(hist.length / 8)) } },
    yAxis: { type: 'value', axisLabel: { formatter: '${value}' } },
    tooltip: { trigger: 'axis' },
    series: [{
      type: 'line', smooth: true, symbol: 'circle', symbolSize: 5,
      data: hist.map(h => h.source ? h.balance : null),
      lineStyle: { color: '#0a84ff', width: 2 },
      areaStyle: { color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [{ offset: 0, color: 'rgba(10,132,255,0.14)' }, { offset: 1, color: 'rgba(10,132,255,0)' }] } },
    }],
  }
})

function fmtBalance(ch) {
  if (ch.balance == null) return null
  return `$${Number(ch.balance).toFixed(2)}`
}

// ===== 新增 / 编辑 =====
function openAdd() {
  editing.value = null
  form.value = emptyForm()
  // 默认归入默认分组
  const dg = groups.value.find(g => g.name === '默认分组')
  form.value.group_ids = dg ? [dg.id] : []
  testResult.value = null
  editExpectedUpdatedAt.value = null
  balanceTokenStatus.value = null
  showModal.value = true
}

function openEdit(ch) {
  editing.value = ch
  // 乐观锁：记录打开弹窗时看到的 updated_at（来自列表项或详情对象；若详情经 getChannel 刷新则以刷新值为准）
  editExpectedUpdatedAt.value = ch.updated_at || null
  balanceTokenStatus.value = ch.balance_api_token_configured != null
    ? { configured: !!ch.balance_api_token_configured, suffix: ch.balance_api_token_suffix || '' }
    : null
  form.value = {
    name: ch.name, base_url: ch.base_url, access_token: '', api_key: '',
    protocol: ch.protocol || 'openai',
    relay_type: ch.relay_type || 'custom',
    role: ch.role, user_priority: ch.user_priority, weight: ch.weight || 1,
    daily_probe_budget: ch.daily_probe_budget || 0,
    balance_api_url: ch.balance_api_url || '',
    balance_api_token: '',
    model_pairs: Object.entries(ch.model_mapping || {}).map(([k, v]) => ({ from: k, to: v })),
    capabilities: ch.capabilities || [],
    group_ids: (ch.groups || []).map(g => g.id),
  }
  if (!form.value.model_pairs.length) form.value.model_pairs.push({ from: '', to: '' })
  testResult.value = null
  showModal.value = true
}

function addPair() { form.value.model_pairs.push({ from: '', to: '' }) }
function removePair(i) { form.value.model_pairs.splice(i, 1) }
const caps = ['tools', 'vision', 'audio', 'json_mode', 'function_calling']
function toggleCap(cap) {
  const i = form.value.capabilities.indexOf(cap)
  if (i >= 0) form.value.capabilities.splice(i, 1)
  else form.value.capabilities.push(cap)
}

async function save() {
  if (!form.value.name || !form.value.base_url) {
    toast('请填写站点名称与 Base URL', 'error')
    return
  }
  if (!editing.value && (!form.value.access_token || !form.value.api_key)) {
    toast('新建站点需要填写 Access Token 与 API Key', 'error')
    return
  }
  saving.value = true
  try {
    const mm = {}
    form.value.model_pairs.filter(p => p.from).forEach(p => { mm[p.from] = p.to || p.from })
    const payload = {
      name: form.value.name, base_url: form.value.base_url,
      protocol: form.value.protocol || 'openai',
      relay_type: form.value.relay_type || 'custom',
      role: form.value.role, user_priority: form.value.user_priority, weight: form.value.weight,
      daily_probe_budget: form.value.daily_probe_budget, model_mapping: mm, capabilities: form.value.capabilities,
      balance_api_url: form.value.balance_api_url || '',
      group_ids: form.value.group_ids,
    }
    if (!editing.value || form.value.balance_api_token) {
      payload.balance_api_token = form.value.balance_api_token || ''
    }
    // 编辑时凭证留空 = 保持不变（后端不返回明文凭证）
    if (form.value.access_token) payload.access_token = form.value.access_token
    if (form.value.api_key) payload.api_key = form.value.api_key
    if (editing.value) {
      // 不带 expected_updated_at：checker 后台进程会持续更新站点数据（健康/余额），
      // 乐观锁在这种情况下会误报。真正的并发编辑冲突概率极低，由后端自行处理。
      await api.updateChannel(editing.value.id, payload)
    } else {
      await api.createChannel(payload)
    }
    toast(editing.value ? '站点已更新' : '站点已添加', 'success')
    showModal.value = false
    await load()
  } catch (e) {
    if (e?.status === 409) {
      // 乐观锁冲突：后台 checker 或其他会话更新了该站点
      // 自动刷新数据并重新打开弹窗，保留用户已编辑的表单内容
      toast('站点数据已被后台更新，已为你刷新最新数据', 'info')
      const savedForm = { ...form.value }
      const savedExpected = editExpectedUpdatedAt.value
      await load()
      // 重新找到该站点并重新打开编辑弹窗
      const refreshed = channels.value.find(c => c.id === editing.value?.id)
      if (refreshed) {
        openEdit(refreshed)
        // 恢复用户已编辑的表单内容（但用最新的 updated_at 作为乐观锁）
        form.value = savedForm
        editExpectedUpdatedAt.value = refreshed.updated_at || savedExpected
      }
    }
  } finally { saving.value = false }
}

async function testConn() {
  testing.value = true
  testResult.value = null
  try {
    const isAnthropic = form.value.protocol === 'anthropic'
    const headers = isAnthropic
      ? { 'x-api-key': form.value.api_key, 'anthropic-version': '2023-06-01', 'content-type': 'application/json' }
      : { Authorization: `Bearer ${form.value.api_key}` }
    const resp = await fetch(form.value.base_url.replace(/\/+$/, '') + '/v1/models', { headers })
    if (resp.ok) testResult.value = { ok: true, msg: '上游连接成功' }
    else testResult.value = { ok: false, msg: `HTTP ${resp.status}` }
  } catch (e) {
    testResult.value = { ok: false, msg: e.message }
  }
  finally { testing.value = false }
}

// ===== 上游模型列表 =====
async function fetchUpstreamModels() {
  modelsLoading.value = true
  modelsError.value = null
  upstreamModels.value = []
  try {
    let list = []
    if (modelsSource.value === 'detail' && selected.value) {
      const r = await api.channelModels(selected.value.id)
      list = r.models || []
    } else {
      // 表单模式：用表单里的地址与凭据经后端探测（避免浏览器 CORS 限制）
      if (!form.value.base_url) { modelsError.value = '请先填写 Base URL'; return }
      const r = await api.probeUpstreamModels(form.value.base_url, form.value.api_key, form.value.protocol || 'openai')
      list = r.models || []
    }
    upstreamModels.value = list
    if (!list.length) modelsError.value = '上游未返回任何模型'
  } catch (e) {
    modelsError.value = e.message
  } finally { modelsLoading.value = false }
}

// 详情模式：把模型写入站点 model_mapping（映射到自身）
async function applyModelMapping(models) {
  if (!models.length) return
  try {
    const mm = { ...(selected.value?.model_mapping || {}) }
    models.forEach(m => { mm[m.id] = m.id })
    await api.updateChannel(selected.value.id, { model_mapping: mm })
    toast(`已添加 ${models.length} 个模型映射`, 'success')
    showModels.value = false
    await load()
  } catch { /* 已提示 */ }
}

// 表单模式：追加到表单的 model_pairs（本地）
function appendModelToForm(models) {
  if (!models.length) return
  let added = 0
  models.forEach(m => {
    if (!form.value.model_pairs.some(p => p.from === m.id)) {
      form.value.model_pairs.push({ from: m.id, to: m.id })
      added++
    }
  })
  toast(added ? `已添加 ${added} 个映射，保存后生效` : '这些模型已在映射中', added ? 'success' : 'info')
  showModels.value = false
}

function mapOne(m) {
  if (modelsSource.value === 'detail') applyModelMapping([m])
  else appendModelToForm([m])
}

function mapAll() {
  if (modelsSource.value === 'detail') applyModelMapping(upstreamModels.value)
  else appendModelToForm(upstreamModels.value)
}

async function setDefault(m) {
  if (!selected.value) return
  try {
    await api.updateChannel(selected.value.id, { test_model: m.id })
    toast(`「${m.id}」已设为「${selected.value.name}」的默认测试模型`, 'success')
    await load()
  } catch { /* 已提示 */ }
}

async function toggleChannel(ch) {
  try {
    await api.updateChannel(ch.id, { enabled: !ch.enabled })
    toast(ch.enabled ? '站点已禁用' : '站点已启用', 'success')
    await load()
  } catch { /* 已提示 */ }
}

// 删除站点确认
const confirmDeleteChannel = ref(null)

function askRemoveChannel(ch) {
  confirmDeleteChannel.value = ch
}

async function doRemoveChannel() {
  const ch = confirmDeleteChannel.value
  confirmDeleteChannel.value = null
  try {
    await api.deleteChannel(ch.id)
    toast('站点已删除', 'success')
    if (selected.value?.id === ch.id) selected.value = null
    await load()
  } catch { /* 已提示 */ }
}

onMounted(() => { load(); loadGroups() })
</script>

<template>
  <div class="page-wrap fade-in">
    <div class="page-head">
      <div>
        <div class="page-title">站点</div>
        <div class="page-sub">管理上游 API 站点（中转渠道）</div>
      </div>
      <div class="row gap-2">
        <button class="btn btn-ghost" @click="load" :disabled="loading"><Icon name="refresh" :size="15" />刷新</button>
        <button class="btn btn-ghost" @click="openGroupModal()"><Icon name="layers" :size="15" />管理分组</button>
        <button class="btn btn-primary" @click="openAdd"><Icon name="plus" :size="15" />添加站点</button>
      </div>
    </div>

    <!-- 分组筛选 chips -->
    <div class="row gap-2 mb-3" style="flex-wrap:wrap">
      <button class="seg" :class="{ on: groupFilter == null }" @click="groupFilter = null">全部站点</button>
      <button v-for="g in groups" :key="g.id" class="seg" :class="{ on: groupFilter === g.id }" @click="groupFilter = g.id">
        {{ g.name }}<span class="seg-count">{{ g.channel_count }}</span>
      </button>
    </div>

    <div class="ch-layout">
      <!-- 左侧列表 -->
      <div class="ch-list">
        <div class="row gap-2 mb-2">
          <div class="grow" style="position:relative">
            <span style="position:absolute;left:11px;top:50%;transform:translateY(-50%);color:var(--text-3);display:flex"><Icon name="search" :size="14" /></span>
            <input v-model="search" class="input" placeholder="搜索站点" style="padding-left:33px">
          </div>
          <SelectBox v-model="filter" :options="filterOpts" width="104px" style="flex-shrink:0" />
        </div>

        <div v-if="loading" class="col" style="gap:10px">
          <div v-for="i in 3" :key="i" class="card skeleton" style="height:96px" />
        </div>
        <EmptyState v-else-if="filtered.length === 0" icon="server" title="暂无站点" desc="点击右上角「添加站点」创建第一个上游渠道" style="padding:40px 0" />
        <div
          v-for="ch in filtered" :key="ch.id"
          class="card ch-card" :class="{ selected: selected?.id === ch.id }"
          @click="select(ch)"
        >
          <div class="row gap-2">
            <span :class="dotClass(ch)" />
            <span class="grow truncate" style="font-size:14px;font-weight:600">{{ ch.name }}</span>
            <span class="badge" :class="protocolBadge(ch.protocol)" :title="ch.protocol === 'anthropic' ? 'Claude 原生协议，网关自动转换' : 'OpenAI 兼容协议'">{{ protocolLabel(ch.protocol) }}</span>
            <span v-if="relayTypeBadge(ch.relay_type)" class="badge" :class="relayTypeBadge(ch.relay_type)">{{ relayTypeLabel(ch.relay_type) }}</span>
            <span class="badge" :class="roleBadge(ch.role)">{{ roleLabelCn(ch.role) }}</span>
          </div>
          <div class="row gap-2 mt-1">
            <span class="ch-stat mono"><b>{{ fmtNum(ch.requests_24h) }}</b> 请求</span>
            <span class="ch-stat mono"><b :style="{ color: ch.success_rate == null ? 'inherit' : ch.success_rate > 0.95 ? 'var(--green)' : ch.success_rate > 0.8 ? 'var(--orange)' : 'var(--red)' }">{{ ch.success_rate != null ? fmtPct(ch.success_rate) : '—' }}</b> 成功率</span>
            <span class="ch-stat mono"><b>{{ fmtMs(ch.avg_latency_ms) }}</b> 延迟</span>
          </div>
          <div class="row gap-2 mt-1">
            <span v-for="g in ch.groups || []" :key="g.id" class="badge badge-teal" style="font-size:10.5px">{{ g.name }}</span>
            <span class="text-3 mono" style="margin-left:auto;font-size:11px">P{{ ch.user_priority }}</span>
          </div>
          <div class="row gap-2 mt-1">
            <span class="badge" :class="circuitBadge(ch.circuit_state).cls">{{ circuitBadge(ch.circuit_state).label }}</span>
            <span class="badge" :class="ch.enabled ? 'badge-green' : 'badge-gray'">{{ ch.enabled ? '已启用' : '已禁用' }}</span>
            <span v-if="ch.balance != null" class="badge mono" :class="ch.balance <= 1 ? 'badge-red' : 'badge-green'" style="margin-left:auto" :title="'余额来源: ' + (ch.balance_source || '—') + ' · 检测于 ' + (ch.balance_checked_at ? fmtTime(ch.balance_checked_at) : '—')">
              💰 {{ fmtBalance(ch) }}
            </span>
            <span v-else-if="ch.balance_error" class="badge badge-gray" style="margin-left:auto" :title="ch.balance_error">余额不可用</span>
          </div>
        </div>
      </div>

      <!-- 右侧详情 -->
      <div class="card ch-detail">
        <EmptyState v-if="!selected" icon="server" title="选择一个站点" desc="查看详细信息与健康数据" style="margin:auto" />
        <template v-else>
          <div class="ch-detail-head">
            <div class="row gap-2">
              <span :class="dotClass(selected)" />
              <span style="font-size:17px;font-weight:700">{{ selected.name }}</span>
              <span class="badge" :class="protocolBadge(selected.protocol)" :title="selected.protocol === 'anthropic' ? 'Claude 原生协议，网关自动转换' : 'OpenAI 兼容协议'">{{ protocolLabel(selected.protocol) }}</span>
              <span v-if="relayTypeBadge(selected.relay_type)" class="badge" :class="relayTypeBadge(selected.relay_type)">{{ relayTypeLabel(selected.relay_type) }}</span>
              <span class="badge" :class="roleBadge(selected.role)">{{ roleLabelCn(selected.role) }}</span>
            </div>
            <div class="row gap-2">
              <button class="btn btn-ghost btn-sm" @click="openEdit(selected)"><Icon name="pencil" :size="13" />编辑</button>
              <button class="btn btn-ghost btn-sm" @click="toggleChannel(selected)"><Icon :name="selected.enabled ? 'zap_off' : 'bolt'" :size="13" />{{ selected.enabled ? '禁用' : '启用' }}</button>
              <button class="btn btn-danger btn-sm" @click="askRemoveChannel(selected)"><Icon name="trash" :size="13" />删除</button>
            </div>
          </div>

          <!-- 页签 -->
          <div class="ch-tabs">
            <button v-for="t in [{k:'info',l:'基本信息'},{k:'health',l:'健康'},{k:'stats',l:'统计'},{k:'balance',l:'余额'},{k:'ratio',l:'倍率'}]" :key="t.k"
              class="ch-tab" :class="{ active: detailTab === t.k }" @click="detailTab = t.k; t.k === 'health' && loadHealth(); t.k === 'balance' && loadBalance(); t.k === 'ratio' && loadRatio()">
              {{ t.l }}
            </button>
          </div>

          <div class="ch-detail-body">
            <!-- 基本信息 -->
            <div v-if="detailTab === 'info'">
              <div class="field"><label class="field-label">Base URL</label><div class="code">{{ selected.base_url }}</div></div>
              <div class="form-grid-2">
                <div class="field"><label class="field-label">中转站类型</label><div class="code">{{ selected.relay_type === 'newapi' ? 'New API（new-api/one-api 系）' : selected.relay_type === 'sub2api' ? 'Sub2API' : '自定义' }}</div></div>
                <div class="field"><label class="field-label">接口协议</label><div class="code">{{ selected.protocol === 'anthropic' ? 'Anthropic · Claude 原生' : 'OpenAI · OpenAI 兼容' }}</div></div>
                <div class="field"><label class="field-label">聊天端点</label><div class="code">{{ (selected.base_url || '').replace(/\/+$/, '') + (selected.protocol === 'anthropic' ? '/v1/messages' : '/v1/chat/completions') }}</div></div>
                <div class="field"><label class="field-label">余额接口</label><div class="code">{{ selected.balance_api_url || '按类型默认' }}</div></div>
                <div class="field"><label class="field-label">用户优先级</label><div class="code">{{ selected.user_priority }}</div></div>
                <div class="field"><label class="field-label">权重</label><div class="code">{{ selected.weight || 1 }}</div></div>
                <div class="field"><label class="field-label">每日探针预算</label><div class="code">${{ selected.daily_probe_budget || 0 }}</div></div>
                <div class="field"><label class="field-label">创建时间</label><div class="code">{{ fmtDate(selected.created_at) }}</div></div>
              </div>
              <div class="field">
                <label class="field-label">支持能力</label>
                <div class="row gap-2" style="flex-wrap:wrap">
                  <span v-for="cap in selected.capabilities || []" :key="cap" class="badge badge-teal">{{ cap }}</span>
                  <span v-if="!(selected.capabilities || []).length" class="text-3" style="font-size:12.5px">暂无</span>
                </div>
              </div>
              <div class="field">
                <label class="field-label">模型映射</label>
                <div class="code" style="max-height:140px;overflow-y:auto">{{ JSON.stringify(selected.model_mapping || {}, null, 2) }}</div>
                <button class="btn btn-ghost btn-sm mt-2" @click="modelsSource='detail'; showModels=true; fetchUpstreamModels()">
                  <Icon name="download" :size="13" />获取上游模型列表
                </button>
              </div>
            </div>

            <!-- 健康 -->
            <div v-else-if="detailTab === 'health'">
              <div v-if="healthLoading" class="skeleton" style="height:200px" />
              <EmptyState v-else-if="health.length === 0" icon="gauge" title="暂无探测记录" desc="请启动 checker 进程后查看" style="padding:40px 0" />
              <div v-else>
                <div class="row gap-2 mb-3" style="flex-wrap:wrap">
                  <span class="health-dot" v-for="h in [...health].reverse().slice(0, 10)" :key="h.id"
                    :class="h.is_alive ? 'ok' : 'fail'" :title="fmtDate(h.checked_at) + (h.is_alive ? ' 存活' : ' 离线')" />
                  <span class="text-3" style="font-size:11.5px">← 最新 · 最近 10 次存活探测</span>
                </div>
                <div class="table-wrap">
                  <table>
                    <thead><tr><th scope="col">时间</th><th scope="col">状态</th><th scope="col">延迟</th><th scope="col">Epoch</th></tr></thead>
                    <tbody>
                      <tr v-for="h in health" :key="h.id">
                        <td class="mono text-3" data-label="时间">{{ fmtDate(h.checked_at) }}</td>
                        <td data-label="状态"><span class="badge" :class="h.is_alive ? 'badge-green' : 'badge-red'">{{ h.is_alive ? '✓ 存活' : '✗ 离线' }}</span></td>
                        <td class="mono" data-label="延迟">{{ h.latency_ms != null ? h.latency_ms + ' ms' : '—' }}</td>
                        <td class="mono text-3" data-label="Epoch">{{ h.epoch }}</td>
                      </tr>
                    </tbody>
                  </table>
                </div>
              </div>
            </div>

            <!-- 统计 -->
            <div v-else-if="detailTab === 'stats'">
              <div class="grid-3 mb-3">
                <div class="card mini-stat"><div class="stat-label">24h 请求</div><div class="stat-value" style="font-size:24px">{{ fmtNum(selected.requests_24h) }}</div></div>
                <div class="card mini-stat"><div class="stat-label">成功率</div><div class="stat-value" style="font-size:24px" :style="{color:selected.success_rate==null?'inherit':selected.success_rate>0.95?'var(--green)':selected.success_rate>0.8?'var(--orange)':'var(--red)'}">{{ selected.success_rate != null ? fmtPct(selected.success_rate) : '—' }}</div></div>
                <div class="card mini-stat"><div class="stat-label">平均延迟</div><div class="stat-value" style="font-size:24px">{{ fmtMs(selected.avg_latency_ms) }}</div></div>
              </div>
              <div v-if="circuitOf(selected.id).length" class="field">
                <label class="field-label">熔断明细（按模型）</label>
                <div class="table-wrap">
                  <table>
                    <thead><tr><th scope="col">模型</th><th scope="col">状态</th><th scope="col">失败/成功</th><th scope="col">冷却截止</th></tr></thead>
                    <tbody>
                      <tr v-for="s in circuitOf(selected.id)" :key="s.model">
                        <td data-label="模型"><span class="badge badge-blue">{{ s.model }}</span></td>
                        <td data-label="状态"><span class="badge" :class="circuitBadge(s.state).cls">{{ circuitBadge(s.state).label }}</span></td>
                        <td class="mono" data-label="失败/成功"><span class="text-red">{{ s.failure_count }}</span> / <span class="text-green">{{ s.success_count }}</span></td>
                        <td class="mono text-3" data-label="冷却截止">{{ s.cooling_until ? fmtDate(s.cooling_until) : '—' }}</td>
                      </tr>
                    </tbody>
                  </table>
                </div>
              </div>
            </div>

            <!-- 倍率 -->
            <div v-else-if="detailTab === 'ratio'">
              <div v-if="ratioLoading" class="skeleton" style="height:180px" />
              <template v-else>
                <!-- 倍率上限设置 -->
                <div class="card card-pad mb-3" style="padding:12px 16px">
                  <div class="row gap-2" style="align-items:center;flex-wrap:wrap">
                    <Icon name="alert" :size="15" :style="{ color: overLimitNow ? 'var(--red)' : 'var(--text-3)' }" />
                    <label class="field-label" style="margin-bottom:0">倍率上限</label>
                    <input v-model.number="ratioLimitInput" type="number" step="0.01" min="0" class="input" style="width:110px" placeholder="如 2.0">
                    <span class="text-3" style="font-size:12px">x（0 = 不限）</span>
                    <button class="btn btn-ghost btn-sm" :disabled="savingRatioLimit" @click="saveRatioLimit">
                      <Icon name="check" :size="12" />{{ savingRatioLimit ? '保存中…' : '保存' }}
                    </button>
                    <span v-if="overLimitNow" class="badge badge-red">当前实测已超上限</span>
                    <span v-else-if="ratioLimitInput > 0" class="badge badge-green">实测均在限内</span>
                    <span class="text-3" style="font-size:11.5px;margin-left:auto">实测倍率超过上限时，总览「告警」与侧边栏红点会提示</span>
                  </div>
                </div>

                <!-- 实测控制行 -->
                <div class="row gap-2 mb-3" style="flex-wrap:wrap;align-items:flex-end">
                  <div class="field" style="margin-bottom:0">
                    <label class="field-label">实测模型</label>
                    <SelectBox v-model="probeModel" :options="probeModelOpts" width="190px" />
                  </div>
                  <div class="field" style="margin-bottom:0">
                    <label class="field-label">max_tokens</label>
                    <input v-model.number="probeTokens" type="number" min="8" max="256" class="input" style="width:90px">
                  </div>
                  <div v-if="currentModelPrice" class="field" style="margin-bottom:0">
                    <label class="field-label">官网价（$/1M）</label>
                    <div class="code" style="padding:7px 10px;font-size:12px">
                      输入 ${{ Number(currentModelPrice.input_price_per_m).toFixed(2) }} · 输出 ${{ Number(currentModelPrice.output_price_per_m).toFixed(2) }}
                      <span v-if="currentModelPrice.note" class="text-3" style="font-size:10.5px;margin-left:6px" :title="currentModelPrice.note">（可在设置页修改）</span>
                    </div>
                  </div>
                  <div v-else-if="priceMissing" class="field" style="margin-bottom:0">
                    <label class="field-label" style="color:var(--orange)">该模型暂无官网价，请声明（$/1M）</label>
                    <div class="row gap-2">
                      <input v-model.number="declaredInPrice" type="number" min="0" step="0.01" class="input" style="width:100px" placeholder="输入价">
                      <input v-model.number="declaredOutPrice" type="number" min="0" step="0.01" class="input" style="width:100px" placeholder="输出价">
                    </div>
                  </div>
                  <button class="btn btn-primary btn-sm" :disabled="probing || !probeModel" @click="runProbe">
                    <Icon name="bolt" :size="13" />{{ probing ? '实测中…' : '立即实测' }}
                  </button>
                  <span class="text-3" style="font-size:11.5px;padding-bottom:7px">真实推理 + 余额差值，花费计入每日探测预算</span>
                </div>

                <!-- 倍率检测分组 -->
                <div class="mb-3">
                  <div class="row gap-2 mb-2" style="align-items:center">
                    <span style="font-size:13px;font-weight:700">倍率分组</span>
                    <button class="btn btn-ghost btn-sm" @click="openRatioGroupModal()"><Icon name="plus" :size="12" />新建分组</button>
                    <button v-if="(ratio?.groups || []).length" class="btn btn-ghost btn-sm" :disabled="probingAll || probingGroupId != null" @click="probeAllGroups">
                      <Icon name="bolt" :size="12" />{{ probingAll ? '批量实测中…' : '一键实测全部组' }}
                    </button>
                    <span class="text-3" style="font-size:11.5px;margin-left:auto">每组实测其默认检测模型</span>
                  </div>
                  <div v-if="!(ratio?.groups || []).length" class="text-3" style="font-size:12.5px;margin-bottom:6px">
                    尚未定义分组。上游通常有多个模型倍率组（如特惠组/高级组），在此为每组指定模型与默认检测模型，实测时以默认模型代表整组倍率。
                  </div>
                  <div v-for="g in ratio?.groups || []" :key="g.id" class="card card-pad mb-2" style="padding:12px 16px">
                    <div class="row gap-2" style="align-items:center">
                      <span style="font-size:13.5px;font-weight:700">{{ g.name }}</span>
                      <span class="badge badge-blue">默认检测: {{ g.default_model || '—' }}</span>
                      <span v-if="g.default_ratio != null" class="badge mono badge-teal">代表倍率 {{ Number(g.default_ratio).toFixed(4) }}x</span>
                      <span v-else class="badge badge-gray">未实测</span>
                      <span class="row gap-1" style="margin-left:auto">
                        <button class="btn btn-ghost btn-sm" :disabled="probingGroupId === g.id || probingAll" @click="probeGroup(g)">
                          <Icon name="bolt" :size="12" />{{ probingGroupId === g.id ? '实测中…' : '实测' }}
                        </button>
                        <button class="btn btn-ghost btn-sm" @click="openRatioGroupModal(g)"><Icon name="pencil" :size="12" /></button>
                        <button class="btn btn-ghost btn-sm" :aria-label="'删除 ' + g.name" @click="askDeleteRatioGroup(g)"><Icon name="trash" :size="12" /></button>
                      </span>
                    </div>
                    <div v-if="g.default_checked_at" class="text-3" style="font-size:11px;margin-top:4px">
                      代表倍率实测于 {{ fmtDate(g.default_checked_at) }}（{{ g.default_source === 'manual' ? '手动' : '定时' }}）
                    </div>
                    <div class="row gap-2 mt-2" style="flex-wrap:wrap">
                      <span v-for="m in g.members || []" :key="m.model" class="badge mono" :class="m.model === g.default_model ? 'badge-teal' : 'badge-gray'" :title="m.real_ratio != null ? '实测 ' + Number(m.real_ratio).toFixed(4) + 'x（' + (m.source === 'manual' ? '手动' : '定时') + '）' : '未实测'">
                        {{ m.model }}<template v-if="m.real_ratio != null"> · {{ Number(m.real_ratio).toFixed(4) }}x</template>
                      </span>
                    </div>
                  </div>
                </div>

                <!-- 实测结果卡 -->
                <div v-if="probeResult" class="card card-pad mb-3" style="padding:14px 18px">
                  <div class="row gap-3" style="flex-wrap:wrap;align-items:center">
                    <div style="min-width:130px">
                      <div class="stat-label">实测倍率 · {{ probeResult.model }}</div>
                      <div class="stat-value" style="font-size:30px;color:var(--blue)">{{ Number(probeResult.real_ratio).toFixed(4) }}x</div>
                      <div class="stat-foot" style="font-size:11.5px">
                        <template v-if="probeResult.basis === 'official'">
                          官网价 {{ Number(probeResult.official_input_per_m).toFixed(2) }}/{{ Number(probeResult.official_output_per_m).toFixed(2) }} $/1M
                        </template>
                        <template v-else>官方价未知，按 $10/1M 混合基准估算</template>
                      </div>
                    </div>
                    <div v-if="probeResult.basis === 'official'" class="text-3 mono" style="font-size:12px;line-height:1.7">
                      推算实际单价：输入 ${{ Number(probeResult.estimated_input_per_m).toFixed(2) }}/1M · 输出 ${{ Number(probeResult.estimated_output_per_m).toFixed(2) }}/1M
                    </div>
                    <div v-if="probeResult.drift_pct != null" class="badge" :class="Math.abs(probeResult.drift_pct) > 30 ? 'badge-red' : 'badge-green'">
                      相对声明 {{ fmtDrift(probeResult.drift_pct) }}
                    </div>
                    <span class="badge" :class="probeResult.basis === 'official' ? 'badge-teal' : 'badge-gray'">{{ probeResult.basis === 'official' ? '相对官网价' : '基准估测' }}</span>
                    <div class="text-3 mono" style="font-size:12px;line-height:1.7">
                      扣费 ${{ Number(probeResult.cost).toFixed(4) }} · tokens {{ probeResult.prompt_tokens }}+{{ probeResult.completion_tokens }}<br/>
                      TTFT {{ probeResult.ttft_ms }}ms · 余额 ${{ Number(probeResult.balance_before).toFixed(2) }} → ${{ Number(probeResult.balance_after).toFixed(2) }}
                    </div>
                  </div>
                  <div v-if="probeResult.warning" class="mt-2" style="color:var(--orange);font-size:12px">⚠ {{ probeResult.warning }}</div>
                </div>

                <!-- 上次实测 -->
                <div v-if="ratio?.latest?.[probeModel]" class="mb-3 text-3" style="font-size:12px">
                  上次实测（{{ ratio.latest[probeModel].source === 'manual' ? '手动' : '定时' }}）：
                  <b class="mono">{{ Number(ratio.latest[probeModel].real_ratio).toFixed(4) }}x</b>
                  · {{ fmtDate(ratio.latest[probeModel].checked_at) }}
                </div>

                <!-- 实测历史折线 -->
                <div v-if="(ratio?.history || []).filter(h => h.model === probeModel).length >= 2" class="card card-pad mb-3" style="padding:14px 18px">
                  <div class="field-label mb-1">实测倍率历史（{{ probeModel }}）</div>
                  <BaseChart :option="ratioChartOption" height="170px" />
                </div>

                <!-- 声明倍率表 -->
                <div v-if="(ratio?.declared || []).length" class="field">
                  <label class="field-label">声明倍率（/api/pricing 同步）</label>
                  <div class="table-wrap">
                    <table>
                      <thead><tr><th scope="col">模型</th><th scope="col">输入倍率</th><th scope="col">输出倍率</th><th scope="col">换算单价（输入/输出）</th><th scope="col">同步时间</th></tr></thead>
                      <tbody>
                        <tr v-for="d in ratio.declared" :key="d.model">
                          <td data-label="模型"><span class="badge badge-blue">{{ d.model }}</span></td>
                          <td class="mono" data-label="输入倍率">{{ Number(d.prompt_ratio).toFixed(2) }}x</td>
                          <td class="mono" data-label="输出倍率">{{ Number(d.completion_ratio).toFixed(2) }}x</td>
                          <td class="mono text-3" data-label="换算单价">${{ Number(d.prompt_price_per_m).toFixed(2) }} / ${{ Number(d.completion_price_per_m).toFixed(2) }} /1M</td>
                          <td class="mono text-3" data-label="同步时间">{{ fmtDate(d.checked_at) }}</td>
                        </tr>
                      </tbody>
                    </table>
                  </div>
                </div>
                <div v-else class="text-3" style="font-size:12.5px;margin-bottom:12px">暂无声明倍率（checker 每 10 分钟从 /api/pricing 同步一次）</div>

                <!-- 实测记录表 -->
                <div v-if="(ratio?.history || []).length" class="field">
                  <label class="field-label">实测记录（最近 15 条）</label>
                  <div class="table-wrap">
                    <table>
                      <thead><tr><th scope="col">时间</th><th scope="col">模型</th><th scope="col">实测倍率</th><th scope="col">扣费</th><th scope="col">tokens</th><th scope="col">TTFT</th><th scope="col">来源</th></tr></thead>
                      <tbody>
                        <tr v-for="h in (ratio.history || []).slice(0, 15)" :key="h.checked_at + h.model">
                          <td class="mono text-3" data-label="时间">{{ fmtDate(h.checked_at) }}</td>
                          <td data-label="模型"><span class="badge badge-blue">{{ h.model }}</span></td>
                          <td class="mono" data-label="实测倍率" :style="{ fontWeight: 600, color: 'var(--blue)' }">{{ Number(h.real_ratio).toFixed(4) }}x</td>
                          <td class="mono" data-label="扣费">${{ Number(h.cost).toFixed(4) }}</td>
                          <td class="mono" data-label="tokens">{{ h.tokens_used }}</td>
                          <td class="mono" data-label="TTFT">{{ h.ttft_ms }}ms</td>
                          <td data-label="来源"><span class="badge" :class="h.source === 'manual' ? 'badge-teal' : 'badge-gray'">{{ h.source === 'manual' ? '手动' : '定时' }}</span></td>
                        </tr>
                      </tbody>
                    </table>
                  </div>
                </div>
                <EmptyState v-if="!(ratio?.history || []).length && !(ratio?.declared || []).length" icon="gauge" title="暂无倍率数据" desc="点击「立即实测」获取真实倍率，或等待定时探针（每小时）" style="padding:30px 0" />
              </template>
            </div>

            <!-- 余额 -->
            <div v-else>
              <div v-if="balanceLoading" class="skeleton" style="height:180px" />
              <template v-else-if="balance?.latest">
                <div class="row gap-4 mb-3" style="flex-wrap:wrap">
                  <div class="card mini-stat" style="min-width:180px">
                    <div class="stat-label">当前余额</div>
                    <div class="stat-value" style="font-size:26px" :style="{ color: balance.latest.balance <= 1 ? 'var(--red)' : balance.latest.balance <= 5 ? 'var(--orange)' : 'var(--green)' }">
                      ${{ Number(balance.latest.balance).toFixed(2) }}
                    </div>
                    <div class="stat-foot" style="margin-top:8px;font-size:11px">检测于 {{ fmtDate(balance.latest.checked_at) }}</div>
                  </div>
                  <div class="card mini-stat" style="min-width:140px">
                    <div class="stat-label">接口类型</div>
                    <div class="stat-value" style="font-size:20px">{{ balance.latest.source === 'oneapi' ? 'one-api / new-api' : balance.latest.source === 'openai' ? 'OpenAI 官方' : balance.latest.source || '—' }}</div>
                  </div>
                  <div class="row gap-2" style="padding-top:10px">
                    <button class="btn btn-ghost btn-sm" @click="loadBalance"><Icon name="refresh" :size="13" />立即检测</button>
                  </div>
                </div>
                <div v-if="(balance.history || []).filter(h => h.source).length >= 2" class="card card-pad mb-3" style="padding:14px 18px">
                  <BaseChart :option="balanceChartOption" height="170px" />
                </div>
                <div class="table-wrap">
                  <table>
                    <thead><tr><th scope="col">时间</th><th scope="col">余额</th><th scope="col">来源</th><th scope="col">状态</th></tr></thead>
                    <tbody>
                      <tr v-for="h in (balance.history || []).slice(0, 15)" :key="h.id">
                        <td class="mono text-3" data-label="时间">{{ fmtDate(h.checked_at) }}</td>
                        <td class="mono" data-label="余额" :style="{ fontWeight: 600 }">${{ Number(h.balance).toFixed(2) }}</td>
                        <td class="text-3" style="font-size:12px" data-label="来源">{{ h.source || '—' }}</td>
                        <td data-label="状态">
                          <span v-if="h.source" class="badge badge-green">✓ 成功</span>
                          <span v-else class="badge badge-gray" :title="h.error">不可用</span>
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </div>
              </template>
              <EmptyState v-else icon="layers" title="暂无余额记录" desc="请启动 checker 进程，余额每 10 分钟自动检测一次" style="padding:40px 0" />
            </div>
          </div>
        </template>
      </div>
    </div>

    <!-- 新增/编辑弹窗 -->
    <BaseModal v-if="showModal" :title="editing ? '编辑站点' : '添加站点'" width="560px" @close="showModal = false">
      <div class="field"><label class="field-label">站点名称 *</label><input v-model="form.name" class="input" placeholder="channel-primary-01"></div>
      <div class="field"><label class="field-label">Base URL *</label><input v-model="form.base_url" class="input mono" placeholder="https://api.example-relay.com"></div>
      <div class="form-grid-2">
        <div class="field">
          <label class="field-label">中转站类型</label>
          <SelectBox v-model="form.relay_type" :options="relayTypeOpts" @change="onRelayTypeChange" />
          <div class="field-hint">选择类型后自动填入对应的余额接口地址（可手动修改）。</div>
        </div>
        <div class="field">
          <label class="field-label">接口协议</label>
          <SelectBox v-model="form.protocol" :options="protocolOpts" />
        </div>
      </div>
      <div class="field">
        <label class="field-label">聊天端点（自动推导）</label>
        <div class="code">{{ form.base_url ? form.base_url.replace(/\/+$/, '') + (form.protocol === 'anthropic' ? '/v1/messages' : '/v1/chat/completions') : '—' }}</div>
      </div>
      <div class="field-hint" style="margin-top:-8px;margin-bottom:8px">Anthropic 协议站点使用 x-api-key 认证，网关会自动完成 OpenAI↔Anthropic 请求/响应/流式转换；余额探测与健康检查不受影响。</div>
      <div class="form-grid-2">
        <div class="field"><label class="field-label">Access Token {{ editing ? '' : '*' }}</label><input v-model="form.access_token" type="password" class="input mono" :placeholder="editing ? '留空则保持不变' : 'Bearer sk-…'"></div>
        <div class="field"><label class="field-label">API Key {{ editing ? '' : '*' }}</label><input v-model="form.api_key" type="password" class="input mono" :placeholder="editing ? '留空则保持不变' : 'sk-…'"></div>
      </div>
      <div class="form-grid-2">
        <div class="field">
          <label class="field-label">角色</label>
          <SelectBox v-model="form.role" :options="roleOpts" />
        </div>
        <div class="field"><label class="field-label">每日探针预算（$）</label><input v-model.number="form.daily_probe_budget" type="number" step="0.1" min="0" class="input"></div>
      </div>
      <div class="field">
        <label class="field-label">余额接口地址</label>
        <input v-model="form.balance_api_url" class="input mono" placeholder="按中转站类型自动填入；也可手动填写完整 URL 或路径">
        <div class="field-hint">选择「中转站类型」后自动填入默认地址：New API → /api/user/self，Sub2API → /api/v1/auth/me。特殊部署可在网页控制台 F12 → Network 抓包后手动修改。</div>
      </div>
      <div class="field">
        <label class="field-label">余额接口令牌（可选）</label>
        <input v-model="form.balance_api_token" type="password" class="input mono" :placeholder="editing ? '留空则保持不变' : '余额接口的 Bearer 令牌（如控制台会话令牌/系统访问令牌）'">
        <div class="field-hint">
          余额接口要求的 Bearer 令牌与站点调用凭证不同时填这里（F12 抓包该请求的 Authorization 头）。
          <template v-if="editing && balanceTokenStatus">
            <span v-if="balanceTokenStatus.configured" class="text-green">已配置（尾号 {{ balanceTokenStatus.suffix.replace(/^\*+/, '') }}）</span>
            <span v-else class="text-3">未配置</span>
          </template>
        </div>
      </div>
      <div class="form-grid-2">
        <div class="field"><label class="field-label">用户优先级：{{ form.user_priority }}</label><input v-model.number="form.user_priority" type="range" min="1" max="200" style="width:100%;accent-color:var(--blue)"></div>
        <div class="field"><label class="field-label">权重：{{ form.weight }}</label><input v-model.number="form.weight" type="range" min="1" max="10" style="width:100%;accent-color:var(--blue)"></div>
      </div>
      <div class="field">
        <label class="field-label">支持能力</label>
        <div class="row gap-2" style="flex-wrap:wrap">
          <button v-for="cap in caps" :key="cap" type="button"
            class="badge" :class="form.capabilities.includes(cap) ? 'badge-teal' : 'badge-gray'"
            style="cursor:pointer;border:none;font-family:inherit"
            @click="toggleCap(cap)">
            {{ cap }}
          </button>
        </div>
      </div>
      <div class="field">
        <label class="field-label">所属分组（可多选）</label>
        <div class="row gap-2" style="flex-wrap:wrap">
          <button v-for="g in groups" :key="g.id" type="button"
            class="badge" :class="form.group_ids.includes(g.id) ? 'badge-teal' : 'badge-gray'"
            style="cursor:pointer;border:none;font-family:inherit"
            @click="form.group_ids.includes(g.id) ? form.group_ids.splice(form.group_ids.indexOf(g.id), 1) : form.group_ids.push(g.id)">
            {{ g.name }}
          </button>
          <span v-if="!groups.length" class="text-3" style="font-size:12px">暂无分组（保存后自动归入默认分组）</span>
        </div>
      </div>
      <div class="field">
        <div class="row">
          <label class="field-label" style="margin-bottom:0">模型映射（上游模型 → 目标模型）</label>
          <span class="spacer" />
          <button type="button" class="btn btn-ghost btn-sm" style="height:26px;padding:0 10px" @click="modelsSource='form'; showModels=true; fetchUpstreamModels()">
            <Icon name="download" :size="12" />从上游获取
          </button>
        </div>
        <div v-for="(pair, i) in form.model_pairs" :key="i" class="row gap-2 mb-2 mt-2">
          <input v-model="pair.from" class="input mono" placeholder="如 gpt-4o">
          <Icon name="arrow_right" :size="14" style="color:var(--text-3);flex-shrink:0" />
          <input v-model="pair.to" class="input mono" placeholder="上游模型名">
          <button type="button" class="icon-btn" style="width:30px;height:30px;flex-shrink:0" @click="removePair(i)"><Icon name="trash" :size="14" /></button>
        </div>
        <button type="button" class="btn btn-ghost btn-sm" @click="addPair"><Icon name="plus" :size="13" />添加映射</button>
      </div>
      <div class="row gap-2 mt-2">
        <button type="button" class="btn btn-ghost btn-sm" @click="testConn" :disabled="testing">
          <Icon name="plug" :size="13" />{{ testing ? '测试中…' : '测试连接' }}
        </button>
        <span v-if="testResult" :class="testResult.ok ? 'text-green' : 'text-red'" style="font-size:12.5px">
          {{ testResult.ok ? '✓ ' + testResult.msg : '✗ ' + testResult.msg }}
        </span>
      </div>
      <template #footer>
        <button class="btn btn-ghost" @click="showModal = false">取消</button>
        <button class="btn btn-primary" @click="save" :disabled="saving">{{ saving ? '保存中…' : '保存' }}</button>
      </template>
    </BaseModal>

    <!-- 上游模型列表弹窗 -->
    <BaseModal v-if="showModels"
      :title="modelsSource === 'detail' ? `上游模型 · ${selected?.name}` : '上游模型 · 表单地址'"
      width="520px" @close="showModels = false">
      <!-- 头部操作 -->
      <div class="row gap-2 mb-3">
        <button class="btn btn-ghost btn-sm" @click="fetchUpstreamModels" :disabled="modelsLoading">
          <Icon name="refresh" :size="13" />{{ modelsLoading ? '获取中…' : '重新获取' }}
        </button>
        <span v-if="!modelsLoading && !modelsError" class="text-3" style="font-size:12.5px">共 {{ upstreamModels.length }} 个模型</span>
        <span class="spacer" />
        <button v-if="!modelsLoading && !modelsError && upstreamModels.length" class="btn btn-primary btn-sm" @click="mapAll">
          <Icon name="plus" :size="13" />全部映射到自身
        </button>
      </div>

      <!-- 加载/错误/空 -->
      <div v-if="modelsLoading" class="skeleton" style="height:220px" />
      <EmptyState v-else-if="modelsError" icon="alert" title="获取失败" :desc="modelsError" style="padding:36px 0" />
      <EmptyState v-else-if="upstreamModels.length === 0" icon="server" title="上游未返回模型" style="padding:36px 0" />

      <!-- 模型列表 -->
      <div v-else class="model-list">
        <div v-for="(m, i) in upstreamModels" :key="m.id" class="row gap-3 model-row">
          <span class="mono text-3" style="width:22px;font-size:11px">{{ i + 1 }}</span>
          <span class="grow mono truncate" style="font-size:13px" :title="m.id">
            {{ m.id }}
            <span v-if="modelsSource === 'detail' && selected?.test_model === m.id" class="badge badge-blue" style="margin-left:6px;font-size:9.5px">该站点默认测试模型</span>
          </span>
          <span v-if="m.object" class="badge badge-gray">{{ m.object }}</span>
          <button v-if="modelsSource === 'detail'" class="btn btn-ghost btn-sm" :class="{ 'btn-primary': selected?.test_model === m.id }" @click="setDefault(m)" :title="`设为「${selected?.name}」的默认测试模型`">
            <Icon name="beaker" :size="12" />默认
          </button>
          <button class="btn btn-ghost btn-sm" @click="mapOne(m)">
            <Icon name="plus" :size="12" />映射自身
          </button>
        </div>
      </div>

      <div class="field-hint mt-3">
        {{ modelsSource === 'detail' ? '「映射自身」会将上游模型名作为目标模型写入该站点的模型映射（保存到服务端）。' : '「映射自身」会将模型追加到下方表单的映射列表，点击「保存」后生效。' }}
      </div>
    </BaseModal>

    <!-- 分组管理弹窗 -->
    <BaseModal v-if="showGroupModal" :title="'分组管理'" width="640px" @close="showGroupModal = false">
      <!-- 分组列表 -->
      <div class="group-list mb-3">
        <div v-for="g in groups" :key="g.id" class="row gap-2 group-row" :class="{ active: editingGroup?.id === g.id }">
          <span class="dot" :class="g.enabled ? 'dot-green' : 'dot-gray'" />
          <span class="grow" style="font-weight:600;font-size:13.5px">{{ g.name }}</span>
          <span v-if="g.default_strategy" class="badge badge-purple">{{ strategyLabel(g.default_strategy) }}</span>
          <span class="badge badge-gray">{{ g.channel_count }} 站点</span>
          <button class="btn btn-ghost btn-sm" @click="openGroupModal(g)">编辑</button>
          <button class="btn btn-danger btn-sm" @click="askDeleteGroup(g)">删除</button>
        </div>
        <div v-if="!groups.length" class="text-3" style="font-size:13px;padding:8px 0">暂无分组</div>
      </div>

      <div class="row gap-2 mb-3">
        <button class="btn btn-ghost btn-sm" @click="openGroupModal()"><Icon name="plus" :size="13" />新建分组</button>
        <span v-if="editingGroup" class="text-3" style="font-size:12.5px">正在编辑：{{ editingGroup.name }}</span>
      </div>

      <div style="border-top:1px solid var(--border);padding-top:16px">
        <div class="form-grid-2">
          <div class="field"><label class="field-label">分组名称 *</label><input v-model="groupForm.name" class="input" placeholder="如：高优组"></div>
          <div class="field" style="display:flex;flex-direction:column;justify-content:flex-end">
            <label class="field-label">路由策略</label>
            <div class="field-hint" style="padding:9px 0 3px">
              已移至 <router-link to="/strategy" class="link" style="font-weight:600">策略中心</router-link> 配置（支持卡片选择与权重自定义）
            </div>
          </div>
          <div class="field" style="grid-column:1/-1"><label class="field-label">描述</label><input v-model="groupForm.description" class="input" placeholder="分组用途说明（可选）"></div>
        </div>

        <div class="field"><label class="field-label">健康检测覆盖（0 = 跟随全局配置）</label>
          <div class="form-grid-2">
            <div><label class="field-hint" style="margin-bottom:4px">存活探测间隔（秒）</label><input v-model.number="groupForm.alive_interval_seconds" type="number" min="0" class="input"></div>
            <div><label class="field-hint" style="margin-bottom:4px">价格同步间隔（秒）</label><input v-model.number="groupForm.pricing_interval_seconds" type="number" min="0" class="input"></div>
            <div><label class="field-hint" style="margin-bottom:4px">推理探针间隔（秒）</label><input v-model.number="groupForm.probe_interval_seconds" type="number" min="0" class="input"></div>
            <div><label class="field-hint" style="margin-bottom:4px">余额检测间隔（秒）</label><input v-model.number="groupForm.balance_interval_seconds" type="number" min="0" class="input"></div>
            <div><label class="field-hint" style="margin-bottom:4px">每日探针预算（$，0 = 全局）</label><input v-model.number="groupForm.daily_probe_budget" type="number" min="0" step="0.1" class="input"></div>
          </div>
        </div>

        <div class="field"><label class="field-label">熔断参数覆盖（0 = 跟随全局配置）</label>
          <div class="form-grid-2">
            <div><label class="field-hint" style="margin-bottom:4px">最小样本数</label><input v-model.number="groupForm.cb_min_samples" type="number" min="0" class="input"></div>
            <div><label class="field-hint" style="margin-bottom:4px">失败率阈值（0~1）</label><input v-model.number="groupForm.cb_open_failure_rate" type="number" min="0" max="1" step="0.05" class="input"></div>
            <div><label class="field-hint" style="margin-bottom:4px">最小失败数</label><input v-model.number="groupForm.cb_open_min_failures" type="number" min="0" class="input"></div>
            <div><label class="field-hint" style="margin-bottom:4px">初始冷却（秒）</label><input v-model.number="groupForm.cb_initial_cooling_seconds" type="number" min="0" class="input"></div>
            <div><label class="field-hint" style="margin-bottom:4px">最大冷却（秒）</label><input v-model.number="groupForm.cb_max_cooling_seconds" type="number" min="0" class="input"></div>
          </div>
        </div>
      </div>

      <template #footer>
        <button class="btn btn-ghost" @click="showGroupModal = false">关闭</button>
        <button class="btn btn-primary" @click="saveGroup" :disabled="savingGroup">{{ savingGroup ? '保存中…' : editingGroup ? '保存修改' : '创建分组' }}</button>
      </template>
    </BaseModal>

    <!-- 删除站点确认 -->
    <ConfirmDialog
      v-if="confirmDeleteChannel"
      title="删除站点"
      :message="`确认删除站点「${confirmDeleteChannel.name}」？其健康数据、熔断状态与请求历史将一并删除，此操作不可撤销。`"
      confirm-text="删除"
      danger
      @confirm="doRemoveChannel"
      @cancel="confirmDeleteChannel = null"
    />

    <!-- 删除分组确认 -->
    <ConfirmDialog
      v-if="confirmDeleteGroup"
      title="删除分组"
      :message="`确认删除分组「${confirmDeleteGroup.name}」？站点与 Key 的分组关联将一并移除（不影响站点本身）。`"
      confirm-text="删除"
      danger
      @confirm="doDeleteGroup"
      @cancel="confirmDeleteGroup = null"
    />

    <!-- 删除倍率分组确认 -->
    <ConfirmDialog
      v-if="confirmDeleteRatioGroup"
      title="删除倍率分组"
      :message="`确认删除倍率分组「${confirmDeleteRatioGroup.name}」？`"
      confirm-text="删除"
      danger
      @confirm="doDeleteRatioGroup"
      @cancel="confirmDeleteRatioGroup = null"
    />

    <!-- 倍率检测分组弹窗 -->
    <BaseModal v-if="showRatioGroupModal" :title="editingRatioGroup ? '编辑倍率分组' : '新建倍率分组'" width="480px" @close="showRatioGroupModal = false">
      <div class="field">
        <label class="field-label">分组名称 *</label>
        <input v-model="ratioGroupForm.name" class="input" placeholder="如：特惠组 / 高级组">
      </div>
      <div class="field">
        <label class="field-label">组内模型 *（可多选，模型可同时属于多个分组）</label>
        <div class="row gap-2" style="flex-wrap:wrap">
          <button v-for="m in modelKeys" :key="m" type="button"
            class="seg" :class="{ on: ratioGroupForm.models.includes(m) }" @click="toggleGroupModel(m)">
            {{ m }}
          </button>
          <span v-if="!modelKeys.length" class="text-3" style="font-size:12px">该站点暂无模型映射，请先在「基本信息」中配置</span>
        </div>
      </div>
      <div class="field">
        <label class="field-label">默认检测模型 *（实测该组倍率时使用的模型）</label>
        <SelectBox v-model="ratioGroupForm.default_model" :options="ratioDefaultModelOpts" placeholder="选择默认检测模型" />
      </div>
      <div class="row gap-2" style="justify-content:flex-end;margin-top:16px">
        <button class="btn btn-ghost" @click="showRatioGroupModal = false">取消</button>
        <button class="btn btn-primary" :disabled="savingRatioGroup" @click="saveRatioGroup">{{ savingRatioGroup ? '保存中…' : '保存' }}</button>
      </div>
    </BaseModal>
  </div>
</template>

<style scoped>
.ch-layout { display: grid; grid-template-columns: 380px 1fr; gap: 16px; min-height: 520px; }
.ch-list { display: flex; flex-direction: column; gap: 10px; overflow-y: auto; max-height: calc(100vh - 190px); padding-right: 2px; }
.ch-card { padding: 14px 16px; cursor: pointer; transition: all var(--dur) var(--ease); }
.ch-card:hover { transform: translateY(-1px); box-shadow: var(--shadow-raised); }
.ch-card.selected { border-color: var(--blue); box-shadow: 0 0 0 3.5px var(--blue-soft); }
.ch-stat { font-size: 11.5px; color: var(--text-3); }
.ch-stat b { color: var(--text-1); font-weight: 600; }

.ch-detail { display: flex; flex-direction: column; overflow: hidden; min-height: 520px; }
.ch-detail-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 18px 22px; border-bottom: 1px solid var(--border); flex-wrap: wrap; }
.ch-tabs { display: flex; gap: 2px; padding: 8px 14px 0; border-bottom: 1px solid var(--border); }
.ch-tab {
  padding: 9px 14px; font-size: 13px; font-weight: 500; color: var(--text-3);
  background: none; border: none; cursor: pointer; font-family: inherit;
  border-bottom: 2px solid transparent; margin-bottom: -1px;
  transition: color var(--dur) var(--ease), border-color var(--dur) var(--ease);
}
.ch-tab:hover { color: var(--text-1); }
.ch-tab.active { color: var(--blue); border-bottom-color: var(--blue); font-weight: 600; }
.ch-detail-body { flex: 1; overflow-y: auto; padding: 20px 22px; }

.mini-stat { padding: 16px 18px; }
.health-dot { width: 12px; height: 12px; border-radius: 4px; display: inline-block; }
.health-dot.ok { background: var(--green); }
.health-dot.fail { background: var(--red); }

.model-list { max-height: 320px; overflow-y: auto; border: 1px solid var(--border); border-radius: var(--radius-md); }
.model-row { padding: 9px 14px; border-bottom: 1px solid var(--border); }
.model-row:last-child { border-bottom: none; }

.group-list { border: 1px solid var(--border); border-radius: var(--radius-md); overflow: hidden; max-height: 200px; overflow-y: auto; }
.group-row { padding: 9px 14px; border-bottom: 1px solid var(--border); }
.group-row:last-child { border-bottom: none; }
.group-row.active { background: var(--blue-soft); }
/* seg/seg-count 样式已移至 base.css 全局 */

@media (max-width: 1000px) {
  .ch-layout { grid-template-columns: 1fr; }
  .ch-list { max-height: 320px; }
}
</style>
