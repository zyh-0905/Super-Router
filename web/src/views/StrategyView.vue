<script setup>
// 策略中心：策略按「系统默认」与「每个分组」分别配置。
//   - 顶部目标选择器：系统默认 / 各分组；
//   - 5 种内置策略卡片，卡片内展示该策略排序时考虑的因素及权重（加权均衡卡显示真实配置权重）；
//   - 分组未单独配置时显示「跟随系统默认」，点卡片即为该分组单独配置，可一键恢复继承；
//   - 保存立即生效（策略与分组策略均按请求实时读取，不缓存）。
import { computed, onMounted, reactive, ref } from 'vue'
import { api } from '../api'
import { toast } from '../store'
import Icon from '../components/Icon.vue'

const loading = ref(true)
const saving = ref(false)

// 配置目标：'system' = 系统默认；'group' = 指定分组
const targetType = ref('system')
const targetGroupId = ref(null)
const targetGroupName = ref('')
const groups = ref([])

// 当前目标的策略状态
const policy = reactive({
  strategy: '',            // 当前生效策略（inherited 时为空）
  source: 'config_file',   // 系统默认来源：database | config_file
  inherited: false,        // 分组是否跟随系统默认
  inheritedStrategy: '',   // 继承时：系统默认策略名
})
// 系统默认策略（分组继承时展示用）
const systemPolicy = reactive({ strategy: '', balancedPercent: { cost: 25, reliability: 25, latency: 25, load: 25 } })

const selected = ref('')     // 选中的卡片 id；'' = 继承状态（无卡片选中）
const weights = reactive({ cost: 25, reliability: 25, latency: 25, load: 25 })

const weightDefs = [
  { key: 'cost', label: '成本优势', hint: '倾向预计费用更低的渠道' },
  { key: 'reliability', label: '可靠性', hint: '倾向近期成功率更高的渠道' },
  { key: 'latency', label: '低延迟', hint: '倾向首字节更快的渠道' },
  { key: 'load', label: '负载均衡', hint: '倾向当前更空闲的渠道' },
]

// 每种策略排序时考虑的因素及占比（文档化展示；balanced 为动态真实权重）
const strategies = [
  {
    id: 'custom_priority', icon: 'layers', name: '手动优先级', tag: '手动掌控',
    desc: '按你在站点里手动设定的「角色层级 → 用户优先级」排序，可靠性/延迟作为其后比较项。适合希望完全人工掌控路由的场景。',
    caption: '依次比较：角色 → 优先级 → 可靠性 → 延迟',
    factors: [
      { label: '角色层级', value: 40 },
      { label: '用户优先级', value: 30 },
      { label: '可靠性', value: 18 },
      { label: '延迟', value: 12 },
    ],
  },
  {
    id: 'price_first', icon: 'bolt', name: '低价优先', tag: '省钱',
    desc: '按预计费用从低到高排序，优先使用最便宜的渠道，适合对价格敏感的场景。',
    caption: '先比价格，相同再比可靠性、延迟',
    factors: [
      { label: '价格', value: 60 },
      { label: '可靠性', value: 25 },
      { label: '延迟', value: 15 },
    ],
  },
  {
    id: 'latency_first', icon: 'gauge', name: '低延迟优先', tag: '速度',
    desc: '按首字节延迟从低到高排序，优先使用响应最快的渠道，适合交互式对话。',
    caption: '先比延迟，相同再比可靠性、价格',
    factors: [
      { label: '延迟', value: 60 },
      { label: '可靠性', value: 25 },
      { label: '价格', value: 15 },
    ],
  },
  {
    id: 'reliability_first', icon: 'check', name: '高可靠优先', tag: '稳定',
    desc: '按近期成功率从高到低排序，优先使用最稳定的渠道，适合对可用性要求高的场景。',
    caption: '先比成功率，相同再比延迟、价格',
    factors: [
      { label: '可靠性', value: 60 },
      { label: '延迟', value: 25 },
      { label: '价格', value: 15 },
    ],
  },
  {
    id: 'balanced', icon: 'chart', name: '加权均衡', tag: '可调权重',
    desc: '成本、可靠性、延迟、负载四维加权打分。与「手动优先级」不同：这里完全由权重自动决定，无需逐个站点手动调优先级。',
    caption: '四维权重由你定义（当前目标下的真实配置）',
    factors: [],
  },
]

const displayName = (id) => strategies.find(s => s.id === id)?.name || id

// 卡片因素条：balanced 动态取当前目标的真实权重
function factorsOf(s) {
  if (s.id !== 'balanced') return s.factors
  const eff = effectiveWeights.value
  return weightDefs.map(d => ({ label: d.label, value: eff[d.key] }))
}

// 当前目标下 balanced 的真实权重（分组继承时取系统默认）
const effectiveWeights = computed(() => {
  if (targetType.value === 'group' && policy.inherited) {
    return { ...systemPolicy.balancedPercent }
  }
  const sum = weights.cost + weights.reliability + weights.latency + weights.load || 1
  return {
    cost: Math.round(weights.cost / sum * 100),
    reliability: Math.round(weights.reliability / sum * 100),
    latency: Math.round(weights.latency / sum * 100),
    load: Math.round(weights.load / sum * 100),
  }
})

const weightSum = computed(() => weights.cost + weights.reliability + weights.latency + weights.load || 1)
const pctOf = (k) => Math.round(weights[k] / weightSum.value * 100)

async function loadSystem() {
  const p = await api.getPolicy()
  systemPolicy.strategy = p.strategy || 'custom_priority'
  systemPolicy.balancedPercent = p.balanced_percent || { cost: 25, reliability: 25, latency: 25, load: 25 }
  if (targetType.value === 'system') {
    policy.strategy = p.strategy || 'custom_priority'
    policy.source = p.source || 'config_file'
    policy.inherited = false
    selected.value = policy.strategy
    Object.assign(weights, p.balanced_percent || { cost: 25, reliability: 25, latency: 25, load: 25 })
  }
}

async function loadGroup() {
  const g = await api.getGroupStrategy(targetGroupId.value)
  targetGroupName.value = g.group_name || ''
  policy.inherited = !!g.inherited
  policy.strategy = g.strategy || ''
  policy.inheritedStrategy = g.inherited_strategy || ''
  if (policy.inherited) {
    selected.value = ''
    Object.assign(weights, { cost: 25, reliability: 25, latency: 25, load: 25 })
  } else {
    selected.value = g.strategy || ''
    Object.assign(weights, g.balanced_percent || { cost: 25, reliability: 25, latency: 25, load: 25 })
  }
}

async function load() {
  loading.value = true
  try {
    const gs = await api.listGroups()
    groups.value = gs.groups || []
    if (targetType.value === 'system') {
      await loadSystem()
    } else {
      await loadSystem() // 分组继承时需要系统默认策略信息
      await loadGroup()
    }
  } catch { /* 错误已由 api 层 toast */ }
  finally { loading.value = false }
}

function switchTarget(type, groupId) {
  if (type === targetType.value && (type === 'system' || groupId === targetGroupId.value)) return
  targetType.value = type
  targetGroupId.value = groupId
  load()
}

function selectStrategy(s) {
  selected.value = s.id
}

function resetWeights() {
  weights.cost = 25
  weights.reliability = 25
  weights.latency = 25
  weights.load = 25
}

async function save() {
  if (!selected.value) return
  saving.value = true
  try {
    const body = { strategy: selected.value }
    if (selected.value === 'balanced') {
      body.balanced_weights = { ...weights }
    }
    if (targetType.value === 'system') {
      await api.updatePolicy(body)
      toast('系统默认策略已保存，立即生效', 'success')
    } else {
      await api.updateGroupStrategy(targetGroupId.value, body)
      toast(`已保存到分组「${targetGroupName.value}」，立即生效`, 'success')
    }
    await load()
  } catch { /* 错误已由 api 层 toast */ }
  finally { saving.value = false }
}

async function restoreInherit() {
  saving.value = true
  try {
    await api.updateGroupStrategy(targetGroupId.value, { strategy: '' })
    toast(`分组「${targetGroupName.value}」已恢复跟随系统默认`, 'success')
    await load()
  } catch { /* 错误已由 api 层 toast */ }
  finally { saving.value = false }
}

onMounted(load)
</script>

<template>
  <div class="page-wrap fade-in">
    <!-- 页头 -->
    <div class="page-head">
      <div>
        <div class="page-title">策略中心</div>
        <div class="page-sub">每个分组可以有自己的路由策略——未单独配置的分组跟随系统默认</div>
      </div>
      <div class="row gap-3" style="flex-wrap:wrap">
        <div v-if="!loading" class="current-pill">
          <span class="pill-dot" :class="policy.inherited ? 'dot-gray' : (targetType === 'system' && policy.source === 'database' ? 'dot-green' : 'dot-blue')" />
          <span v-if="policy.inherited">
            跟随系统默认（当前：<b>{{ displayName(policy.inheritedStrategy) }}</b>）
          </span>
          <span v-else>
            当前生效：<b>{{ displayName(policy.strategy) }}</b>
          </span>
        </div>
        <button v-if="targetType === 'group' && !policy.inherited" class="btn btn-ghost" :disabled="loading || saving" @click="restoreInherit">
          恢复跟随系统默认
        </button>
        <button class="btn btn-primary" :disabled="loading || saving || !selected" @click="save">
          <Icon name="check" :size="14" />
          {{ saving ? '保存中…' : (targetType === 'system' ? '保存为系统默认' : '保存到本分组') }}
        </button>
      </div>
    </div>

    <!-- 配置目标选择器 -->
    <div class="target-bar">
      <span class="target-label">配置对象</span>
      <button class="target-pill" :class="{ active: targetType === 'system' }" @click="switchTarget('system', null)">
        <Icon name="server" :size="13" /> 系统默认
      </button>
      <button
        v-for="g in groups" :key="g.id"
        class="target-pill" :class="{ active: targetType === 'group' && targetGroupId === g.id }"
        @click="switchTarget('group', g.id)"
      >
        {{ g.name }}<span v-if="g.enabled === false" class="target-disabled">已禁用</span>
      </button>
    </div>

    <!-- 继承提示 -->
    <div v-if="!loading && targetType === 'group' && policy.inherited" class="inherit-bar">
      <Icon name="layers" :size="14" />
      <span>分组「{{ targetGroupName }}」未单独配置策略，当前跟随系统默认（{{ displayName(policy.inheritedStrategy) }}）。点击下方任意卡片即可为该分组单独配置。</span>
    </div>

    <div v-if="loading" class="skeleton" style="height:420px" />

    <template v-else>
      <!-- 策略卡片 -->
      <div class="strategy-grid">
        <div
          v-for="s in strategies" :key="s.id"
          class="strategy-card" :class="{ selected: selected === s.id }"
          @click="selectStrategy(s)"
        >
          <div class="card-top">
            <span class="card-icon" :class="'ic-' + s.id"><Icon :name="s.icon" :size="19" /></span>
            <div class="grow">
              <div class="row gap-2">
                <span class="card-name">{{ s.name }}</span>
                <span class="card-tag">{{ s.tag }}</span>
              </div>
            </div>
            <span v-if="selected === s.id" class="sel-badge"><Icon name="check" :size="12" /></span>
            <span v-else class="sel-radio" />
          </div>

          <p class="card-desc">{{ s.desc }}</p>

          <!-- 因素权重 -->
          <div class="factors">
            <div class="factors-cap">
              {{ s.caption }}<span v-if="s.id === 'balanced' && targetType === 'group' && policy.inherited" class="inherit-tag">继承自系统默认</span>
            </div>
            <div v-for="f in factorsOf(s)" :key="s.id + '-' + f.label" class="f-row">
              <span class="f-name">{{ f.label }}</span>
              <div class="f-track">
                <div class="f-bar" :class="'ic-' + s.id" :style="{ width: f.value + '%' }" />
              </div>
              <span class="f-val mono">{{ f.value }}%</span>
            </div>
          </div>
        </div>
      </div>

      <!-- balanced 权重面板（选中加权均衡且非继承状态时出现） -->
      <Transition name="panel">
        <div v-if="selected === 'balanced'" class="weight-panel card card-pad">
          <div class="set-title row gap-2">
            <Icon name="chart" :size="16" />
            <span>定义权重（合计 100%）</span>
            <div class="spacer" />
            <button class="btn btn-ghost btn-sm" @click="resetWeights">恢复均衡</button>
          </div>
          <p class="set-desc">拖动滑块分配四个维度的权重。权重越高的维度，在路由打分中越有话语权。</p>

          <div class="weight-rows">
            <div v-for="d in weightDefs" :key="d.key" class="weight-row">
              <div class="weight-head">
                <span class="weight-label">{{ d.label }}</span>
                <span class="weight-hint">{{ d.hint }}</span>
                <span class="weight-pct mono">{{ pctOf(d.key) }}%</span>
              </div>
              <input
                v-model.number="weights[d.key]" type="range" min="0" max="100" step="1"
                class="weight-slider" :style="{ '--slider-val': pctOf(d.key) + '%' }"
              >
            </div>
          </div>

          <div class="stack-bar">
            <div class="stack-seg seg-cost" :style="{ width: pctOf('cost') + '%' }" />
            <div class="stack-seg seg-rel" :style="{ width: pctOf('reliability') + '%' }" />
            <div class="stack-seg seg-lat" :style="{ width: pctOf('latency') + '%' }" />
            <div class="stack-seg seg-load" :style="{ width: pctOf('load') + '%' }" />
          </div>
          <div class="stack-legend">
            <span><i class="lg lg-cost" />成本 {{ pctOf('cost') }}%</span>
            <span><i class="lg lg-rel" />可靠性 {{ pctOf('reliability') }}%</span>
            <span><i class="lg lg-lat" />延迟 {{ pctOf('latency') }}%</span>
            <span><i class="lg lg-load" />负载 {{ pctOf('load') }}%</span>
          </div>
        </div>
      </Transition>
    </template>
  </div>
</template>

<style scoped>
/* ===== 页头 ===== */
.current-pill {
  display: flex; align-items: center; gap: 8px;
  padding: 9px 14px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-full);
  font-size: 12.5px; color: var(--text-2);
}
.pill-dot { width: 8px; height: 8px; border-radius: 50%; }
.dot-green { background: var(--green); box-shadow: 0 0 6px var(--green); }
.dot-blue { background: var(--blue); box-shadow: 0 0 6px var(--blue); }
.dot-gray { background: var(--text-3); }

/* ===== 配置目标选择器 ===== */
.target-bar {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  margin: 4px 0 16px;
  padding: 10px 14px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
}
.target-label { font-size: 12px; color: var(--text-3); font-weight: 600; margin-right: 2px; }
.target-pill {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 5px 12px;
  border-radius: 999px;
  border: 1px solid var(--border);
  background: var(--surface-raised);
  font-size: 12.5px; font-weight: 600; color: var(--text-2);
  cursor: pointer;
  transition: all 0.15s var(--ease);
}
.target-pill:hover { border-color: var(--blue); color: var(--blue); }
.target-pill.active { background: var(--blue-soft); border-color: transparent; color: var(--blue); }
.target-disabled { font-size: 10px; color: var(--text-3); }

.inherit-bar {
  display: flex; align-items: center; gap: 8px;
  margin: 0 0 14px; padding: 10px 14px;
  background: var(--blue-soft); color: var(--blue);
  border-radius: var(--radius-md);
  font-size: 12.5px; font-weight: 500;
}

/* ===== 卡片 ===== */
.strategy-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 16px;
}
.strategy-card {
  position: relative;
  padding: 18px;
  background: var(--surface);
  border: 1.5px solid var(--border);
  border-radius: var(--radius-xl);
  cursor: pointer;
  transition: transform 0.2s var(--ease), border-color 0.2s var(--ease), box-shadow 0.2s var(--ease);
}
.strategy-card:hover { transform: translateY(-3px); box-shadow: var(--shadow-raised); }
.strategy-card.selected {
  border-color: var(--blue);
  background: linear-gradient(180deg, var(--blue-soft), var(--surface) 55%);
  box-shadow: 0 10px 28px rgba(10, 132, 255, 0.16);
}

.card-top { display: flex; align-items: center; gap: 10px; }
.card-icon {
  display: flex; align-items: center; justify-content: center;
  width: 38px; height: 38px; border-radius: 11px; flex-shrink: 0;
  color: #fff;
}
.ic-custom_priority { background: linear-gradient(135deg, #0a84ff, #64b5ff); }
.ic-price_first { background: linear-gradient(135deg, #30d158, #7ee2a0); }
.ic-latency_first { background: linear-gradient(135deg, #bf5af2, #d59bf8); }
.ic-reliability_first { background: linear-gradient(135deg, #ff9f0a, #ffc46b); }
.ic-balanced { background: linear-gradient(135deg, #ff375f, #ff7a94); }

.card-name { font-size: 14.5px; font-weight: 700; }
.card-tag {
  font-size: 10.5px; font-weight: 700; color: var(--text-3);
  padding: 1.5px 8px; border: 1px solid var(--border); border-radius: 999px;
}
.sel-badge {
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: 50%;
  background: var(--blue); color: #fff;
  animation: popIn 0.3s cubic-bezier(0.34, 1.56, 0.64, 1);
}
@keyframes popIn { from { transform: scale(0.4); opacity: 0; } to { transform: scale(1); opacity: 1; } }
.sel-radio {
  width: 18px; height: 18px; border-radius: 50%;
  border: 2px solid var(--border); background: var(--surface-raised);
}

.card-desc { margin: 12px 0 14px; font-size: 12.5px; line-height: 1.6; color: var(--text-2); min-height: 40px; }

/* ===== 因素权重条 ===== */
.factors { display: flex; flex-direction: column; gap: 6px; }
.factors-cap { font-size: 10.5px; color: var(--text-3); margin-bottom: 2px; }
.inherit-tag {
  margin-left: 6px; padding: 1px 7px;
  background: var(--blue-soft); color: var(--blue);
  border-radius: 999px; font-weight: 600;
}
.f-row { display: flex; align-items: center; gap: 8px; }
.f-name { width: 74px; flex-shrink: 0; font-size: 11.5px; color: var(--text-2); }
.f-track { flex: 1; height: 8px; background: var(--surface-raised); border-radius: 999px; overflow: hidden; }
.f-bar { height: 100%; border-radius: 999px; animation: barGrow 0.7s cubic-bezier(0.22, 1, 0.36, 1) both; }
.f-bar.ic-custom_priority { background: var(--blue); }
.f-bar.ic-price_first { background: #30d158; }
.f-bar.ic-latency_first { background: #bf5af2; }
.f-bar.ic-reliability_first { background: #ff9f0a; }
.f-bar.ic-balanced { background: #ff375f; }
@keyframes barGrow { from { width: 0; } }
.f-val { width: 38px; text-align: right; font-size: 11px; color: var(--text-3); font-variant-numeric: tabular-nums; }

/* ===== 权重面板 ===== */
.weight-panel { margin-top: 18px; }
.weight-rows { display: flex; flex-direction: column; gap: 14px; margin-top: 14px; }
.weight-row { display: flex; flex-direction: column; gap: 6px; }
.weight-head { display: flex; align-items: baseline; gap: 10px; }
.weight-label { font-size: 13px; font-weight: 700; }
.weight-hint { font-size: 11.5px; color: var(--text-3); }
.weight-pct { margin-left: auto; font-size: 13px; font-weight: 700; color: var(--blue); }

.weight-slider {
  -webkit-appearance: none; appearance: none;
  width: 100%; height: 6px; border-radius: 999px;
  background: linear-gradient(90deg, var(--blue) var(--slider-val), var(--surface-raised) var(--slider-val));
  outline: none; cursor: pointer;
}
.weight-slider::-webkit-slider-thumb {
  -webkit-appearance: none; appearance: none;
  width: 18px; height: 18px; border-radius: 50%;
  background: #fff; border: 2.5px solid var(--blue);
  box-shadow: 0 2px 8px rgba(10, 132, 255, 0.4);
  transition: transform 0.15s var(--ease);
}
.weight-slider::-webkit-slider-thumb:hover { transform: scale(1.15); }
.weight-slider::-moz-range-thumb {
  width: 18px; height: 18px; border-radius: 50%;
  background: #fff; border: 2.5px solid var(--blue);
  box-shadow: 0 2px 8px rgba(10, 132, 255, 0.4);
}

.stack-bar { display: flex; height: 12px; border-radius: 999px; overflow: hidden; margin-top: 18px; background: var(--surface-raised); }
.stack-seg { transition: width 0.25s var(--ease); }
.seg-cost { background: #30d158; }
.seg-rel { background: #ff9f0a; }
.seg-lat { background: #bf5af2; }
.seg-load { background: var(--blue); }

.stack-legend {
  display: flex; gap: 14px; flex-wrap: wrap;
  margin-top: 8px; font-size: 11.5px; color: var(--text-2);
}
.stack-legend span { display: inline-flex; align-items: center; gap: 5px; }
.lg { width: 8px; height: 8px; border-radius: 3px; display: inline-block; }
.lg-cost { background: #30d158; } .lg-rel { background: #ff9f0a; }
.lg-lat { background: #bf5af2; } .lg-load { background: var(--blue); }

.panel-enter-active, .panel-leave-active { transition: opacity 0.25s ease, transform 0.25s ease; }
.panel-enter-from, .panel-leave-to { opacity: 0; transform: translateY(10px); }
</style>
