<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api } from '../api'
import { store } from '../store'
import EmptyState from '../components/EmptyState.vue'
import GroupSwitcher from '../components/GroupSwitcher.vue'
import Icon from '../components/Icon.vue'
import { fmtDate, fmtScore, scoreWidth, downloadJSON } from '../utils'

const route = useRoute()
const router = useRouter()

const loading = ref(true)
const decisions = ref([])
const drawer = ref(null)

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
    // 支持 /decisions?id=xxx 深链接
    const id = route.query.id
    if (id) {
      const d = decisions.value.find(x => x.request_id === id)
      if (d) drawer.value = d
    }
  } catch { /* 已提示 */ }
  finally { loading.value = false }
}

function onGroupChange() { load() }

function open(d) { drawer.value = d }

const exclusionLabel = r => ({
  user_disabled: '已禁用', model_not_supported: '不支持该模型', capability_missing: '缺少能力',
  credential_invalid: '凭证失效', quota_exhausted: '配额耗尽', over_price_cap: '超出价格上限',
  circuit_open: '熔断开启', circuit_cooling: '熔断冷却中', circuit_half_open: '熔断半开',
  latency_cap_exceeded: '延迟超限', region_mismatch: '区域不符', protocol_unsupported: '协议不支持',
}[r] || r)

function exportAll() {
  downloadJSON('decisions.json', filtered.value)
}

onMounted(load)
watch(() => route.query.id, load)
</script>

<template>
  <div class="page-wrap fade-in">
    <div class="page-head">
      <div>
        <div class="page-title">决策</div>
        <div class="page-sub">审计每一次路由决策：策略、候选排序与排除原因</div>
      </div>
      <div class="row gap-2">
        <button class="btn btn-ghost" @click="load" :disabled="loading"><Icon name="refresh" :size="15" />刷新</button>
        <button class="btn btn-ghost" @click="exportAll"><Icon name="download" :size="15" />导出 JSON</button>
      </div>
    </div>

    <!-- 筛选 -->
    <div class="card mb-3" style="padding:14px 18px">
      <div class="row gap-2" style="flex-wrap:wrap">
        <GroupSwitcher compact @change="onGroupChange" />
        <div class="grow" style="position:relative;min-width:200px;max-width:280px">
          <span style="position:absolute;left:11px;top:50%;transform:translateY(-50%);color:var(--text-3);display:flex"><Icon name="search" :size="14" /></span>
          <input v-model="search" class="input mono" placeholder="搜索 Request ID" style="padding-left:33px">
        </div>
        <select v-model="modelFilter" class="select" style="width:150px"><option value="">全部模型</option><option v-for="m in models" :key="m" :value="m">{{ m }}</option></select>
        <select v-model="channelFilter" class="select" style="width:150px"><option value="">全部渠道</option><option v-for="c in channelNames" :key="c" :value="c">{{ c }}</option></select>
        <select v-model="strategyFilter" class="select" style="width:170px"><option value="">全部策略</option><option v-for="s in strategies" :key="s" :value="s">{{ s }}</option></select>
      </div>
    </div>

    <!-- 表格 -->
    <div class="card table-wrap">
      <table>
        <thead><tr><th>时间</th><th>Request ID</th><th>模型</th><th>策略</th><th>分组</th><th>选中渠道</th><th>候选</th><th>排除</th><th /></tr></thead>
        <tbody>
          <tr v-if="loading"><td colspan="9"><div class="skeleton" style="height:22px;margin:10px 0" /></td></tr>
          <tr v-if="!loading && filtered.length === 0">
            <td colspan="9"><EmptyState icon="list" title="暂无决策记录" desc="在「测试台」发送请求后，路由决策会出现在这里" style="padding:40px 0" /></td>
          </tr>
          <tr v-for="d in filtered" :key="d.request_id" class="clickable" @click="open(d)">
            <td class="mono text-3 nowrap">{{ fmtDate(d.decided_at) }}</td>
            <td class="mono" style="max-width:150px"><span class="truncate" style="display:block;font-size:11.5px">{{ d.request_id }}</span></td>
            <td><span class="badge badge-blue">{{ d.model }}</span></td>
            <td><span class="badge badge-purple">{{ d.strategy || d.policy_version || '—' }}</span></td>
            <td><span v-if="d.group_name" class="badge badge-teal">{{ d.group_name }}</span><span v-else class="text-3">—</span></td>
            <td style="font-weight:600">{{ d.selected_channel || '—' }}</td>
            <td class="mono text-3">{{ d.candidate_order?.length ?? '—' }}</td>
            <td class="mono" :style="{ color: (d.excluded || []).length ? 'var(--red)' : 'var(--text-3)' }">{{ (d.excluded || []).length }}</td>
            <td style="width:44px"><Icon name="chevron_right" :size="14" style="color:var(--text-3)" /></td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 详情抽屉 -->
    <Teleport to="body">
      <Transition name="drawer">
        <div v-if="drawer" class="drawer-overlay" @mousedown.self="drawer = null">
          <div class="drawer">
            <div class="drawer-head">
              <div class="grow">
                <div style="font-size:16px;font-weight:700">决策详情</div>
                <div class="mono text-3" style="font-size:11px;margin-top:2px">{{ drawer.request_id }}</div>
              </div>
              <button class="icon-btn" @click="drawer = null"><Icon name="x" :size="14" /></button>
            </div>
            <div class="drawer-body">
              <div class="form-grid-2">
                <div class="field"><label class="field-label">模型</label><span class="badge badge-blue">{{ drawer.model }}</span></div>
                <div class="field"><label class="field-label">策略</label><span class="badge badge-purple">{{ drawer.strategy || drawer.policy_version || '—' }}</span></div>
                <div class="field"><label class="field-label">选中渠道</label><div style="font-weight:600">{{ drawer.selected_channel || '—' }}</div></div>
                <div class="field"><label class="field-label">分组</label><span v-if="drawer.group_name" class="badge badge-teal">{{ drawer.group_name }}</span><span v-else class="text-3">—</span></div>
                <div class="field"><label class="field-label">决策时间</label><div class="mono text-3" style="font-size:12px">{{ fmtDate(drawer.decided_at) }}</div></div>
                <div class="field"><label class="field-label">Epoch / 策略版本</label><div class="mono text-3" style="font-size:12px">#{{ drawer.epoch }} · {{ drawer.policy_version }}</div></div>
              </div>

              <div class="field">
                <label class="field-label">决策原因</label>
                <div class="code">{{ drawer.decision_reason || '—' }}</div>
              </div>

              <div v-if="(drawer.candidate_order || []).length" class="field">
                <label class="field-label">候选排序（得分）</label>
                <div v-for="(c, i) in drawer.candidate_order" :key="i" class="row gap-3 cand-row">
                  <span class="mono text-3" style="width:18px">{{ i + 1 }}</span>
                  <span class="grow truncate" style="font-size:13px">{{ c.channel }}</span>
                  <div class="score-track"><div class="score-fill" :style="{ width: scoreWidth(c.score, drawer.candidate_order) + '%' }" /></div>
                  <span class="mono text-3" style="width:56px;text-align:right;font-size:11px">{{ fmtScore(c.score) }}</span>
                  <span v-if="i === 0" class="badge badge-green" style="width:52px;justify-content:center">已选</span>
                  <span v-else style="width:52px" />
                </div>
              </div>

              <div v-if="(drawer.excluded || []).length" class="field">
                <label class="field-label">排除站点</label>
                <div v-for="(e, i) in drawer.excluded" :key="i" class="row gap-2 excl-row">
                  <span class="text-3" style="font-size:12.5px;width:130px;flex-shrink:0">{{ e.channel || '—' }}</span>
                  <span class="badge badge-red">{{ exclusionLabel(e.reason) }}</span>
                </div>
              </div>
            </div>
            <div class="drawer-foot">
              <button class="btn btn-ghost btn-sm" @click="navigator.clipboard.writeText(JSON.stringify(drawer, null, 2))"><Icon name="copy" :size="13" />复制 JSON</button>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
.cand-row { padding: 6px 0; }
.excl-row { padding: 4px 0; }
.score-track { width: 120px; height: 5px; background: var(--border); border-radius: 3px; overflow: hidden; flex-shrink: 0; }
.score-fill { height: 100%; background: var(--blue); border-radius: 3px; transition: width 0.4s ease; }

.drawer-overlay { position: fixed; inset: 0; z-index: var(--z-modal); background: rgba(0,0,0,0.28); backdrop-filter: blur(6px); -webkit-backdrop-filter: blur(6px); }
.drawer {
  position: absolute; right: 0; top: 0; bottom: 0; width: 460px; max-width: 92vw;
  display: flex; flex-direction: column;
  background: var(--surface-raised);
  backdrop-filter: saturate(180%) blur(30px);
  -webkit-backdrop-filter: saturate(180%) blur(30px);
  border-left: 1px solid var(--border);
  box-shadow: -24px 0 64px rgba(0,0,0,0.18);
}
.drawer-head { display: flex; align-items: flex-start; gap: 10px; padding: 18px 20px; border-bottom: 1px solid var(--border); flex-shrink: 0; }
.drawer-body { flex: 1; overflow-y: auto; padding: 18px 20px; }
.drawer-foot { padding: 12px 20px; border-top: 1px solid var(--border); display: flex; justify-content: flex-end; gap: 8px; flex-shrink: 0; }

.drawer-enter-active, .drawer-leave-active { transition: opacity 0.25s ease; }
.drawer-enter-active .drawer, .drawer-leave-active .drawer { transition: transform 0.3s cubic-bezier(0.32, 0.72, 0, 1); }
.drawer-enter-from, .drawer-leave-to { opacity: 0; }
.drawer-enter-from .drawer, .drawer-leave-to .drawer { transform: translateX(100%); }
</style>
