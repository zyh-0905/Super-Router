<script setup>
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { store, toast, saveConnection } from '../store'
import BaseModal from '../components/BaseModal.vue'
import ConfirmDialog from '../components/ConfirmDialog.vue'
import EmptyState from '../components/EmptyState.vue'
import Icon from '../components/Icon.vue'
import { fmtDate } from '../utils'

// 开发环境标识（模板中 import.meta 表达式不可用，统一在此取值）
const isDev = import.meta.env.DEV

// ===== 连接 =====
const connTest = ref(null)
const testing = ref(false)
const keyDraft = ref(store.apiKey)
const baseDraft = ref(store.baseURL)

async function testConn() {
  testing.value = true
  connTest.value = null
  // 用草稿值临时测试
  const saved = { k: store.apiKey, b: store.baseURL }
  store.apiKey = keyDraft.value
  store.baseURL = baseDraft.value
  try {
    const resp = await fetch((store.baseURL || '') + '/health')
    connTest.value = { ok: resp.ok, msg: resp.ok ? '连接正常' : `HTTP ${resp.status}` }
    store.connected = resp.ok
  } catch (e) {
    connTest.value = { ok: false, msg: e.message }
    store.connected = false
  } finally {
    store.apiKey = saved.k
    store.baseURL = saved.b
    testing.value = false
  }
}

function saveConn() {
  store.apiKey = keyDraft.value
  store.baseURL = baseDraft.value.replace(/\/+$/, '')
  saveConnection()
  keyDraft.value = store.apiKey
  baseDraft.value = store.baseURL
  toast('连接设置已保存', 'success')
  api.ping()
}

// ===== API Keys =====
const keys = ref([])
const keysLoading = ref(false)
const showCreateKey = ref(false)
const newKeyRole = ref('caller')
const newKeyResult = ref(null)
const creating = ref(false)
const newKeyGroups = ref([])

// 分组绑定编辑
const groups = ref([])
const showBindGroups = ref(false)
const bindingKey = ref(null)
const bindingGroups = ref([])
const savingBinding = ref(false)

async function loadGroups() {
  try { groups.value = (await api.listGroups()).groups || [] } catch { /* 忽略 */ }
}

async function loadKeys() {
  keysLoading.value = true
  try {
    const r = await api.listKeys()
    keys.value = r.keys || []
  } catch { /* 已提示 */ }
  finally { keysLoading.value = false }
}

async function createKey() {
  creating.value = true
  try {
    const r = await api.createKey(newKeyRole.value, newKeyGroups.value)
    newKeyResult.value = r.key
    loadKeys()
  } catch { /* 已提示 */ }
  finally { creating.value = false }
}

async function toggleKey(k) {
  try {
    await api.updateKey(k.id, !k.enabled)
    k.enabled = !k.enabled
    toast(k.enabled ? 'Key 已启用' : 'Key 已禁用', 'success')
  } catch { /* 已提示 */ }
}

// 撤销 Key 确认
const confirmRevokeKey = ref(null)

function askRevokeKey(k) {
  confirmRevokeKey.value = k
}

async function doRevokeKey() {
  const k = confirmRevokeKey.value
  confirmRevokeKey.value = null
  try {
    await api.deleteKey(k.id)
    keys.value = keys.value.filter(x => x.id !== k.id)
    toast('Key 已撤销', 'success')
  } catch { /* 已提示 */ }
}

function openBindGroups(k) {
  bindingKey.value = k
  bindingGroups.value = (k.groups || []).map(g => g.id)
  showBindGroups.value = true
}

async function saveBinding() {
  savingBinding.value = true
  try {
    await api.updateKey(bindingKey.value.id, null, bindingGroups.value)
    toast('分组绑定已更新', 'success')
    showBindGroups.value = false
    loadKeys()
  } catch { /* 已提示 */ }
  finally { savingBinding.value = false }
}

// ===== 测试台默认模型（每个站点专属） =====
const channels = ref([])
const testModelDrafts = ref({}) // channelId -> 模型名草稿
const savingTestModel = ref(null)

async function loadChannels() {
  try {
    channels.value = (await api.listChannels()).channels || []
    const drafts = {}
    channels.value.forEach(ch => { drafts[ch.id] = ch.test_model || '' })
    testModelDrafts.value = drafts
  } catch { /* 已提示 */ }
}

async function saveChannelTestModel(ch) {
  savingTestModel.value = ch.id
  try {
    await api.updateChannel(ch.id, { test_model: (testModelDrafts.value[ch.id] || '').trim() })
    toast(`「${ch.name}」的默认测试模型已保存`, 'success')
  } catch { /* 已提示 */ }
  finally { savingTestModel.value = null }
}

// ===== 告警设置 =====
const thresholdDraft = ref(1)
const savingThreshold = ref(false)

// ===== 官方模型价格 =====
const modelPrices = ref([])
const pricesLoading = ref(false)
const showPriceModal = ref(false)
const editingPrice = ref(null)
const priceForm = ref({ model: '', input_price_per_m: null, output_price_per_m: null, note: '' })
const savingPrice = ref(false)

async function loadModelPrices() {
  pricesLoading.value = true
  try {
    modelPrices.value = (await api.listModelPrices()).prices || []
  } catch { /* 已提示 */ }
  finally { pricesLoading.value = false }
}

function openPriceModal(p) {
  editingPrice.value = p || null
  priceForm.value = p
    ? {
        model: p.model,
        input_price_per_m: Number(p.input_price_per_m),
        output_price_per_m: Number(p.output_price_per_m),
        cached_read_per_m: p.cached_read_per_m != null ? Number(p.cached_read_per_m) : null,
        cached_write_per_m: p.cached_write_per_m != null ? Number(p.cached_write_per_m) : null,
        note: p.note || '',
      }
    : { model: '', input_price_per_m: null, output_price_per_m: null, cached_read_per_m: null, cached_write_per_m: null, note: '' }
  showPriceModal.value = true
}

async function savePrice() {
  if (!priceForm.value.model) { toast('请填写模型名', 'error'); return }
  if (!(Number(priceForm.value.input_price_per_m) > 0) || !(Number(priceForm.value.output_price_per_m) > 0)) {
    toast('输入/输出价格必须大于 0', 'error'); return
  }
  savingPrice.value = true
  try {
    await api.upsertModelPrice(priceForm.value)
    toast('官方价格已保存（立即生效）', 'success')
    showPriceModal.value = false
    await loadModelPrices()
  } catch { /* 已提示 */ }
  finally { savingPrice.value = false }
}

// 删除价格确认
const confirmDeletePrice = ref(null)

function askRemovePrice(p) {
  confirmDeletePrice.value = p
}

async function doRemovePrice() {
  const p = confirmDeletePrice.value
  confirmDeletePrice.value = null
  try {
    await api.deleteModelPrice(p.model)
    toast('已删除', 'success')
    await loadModelPrices()
  } catch { /* 已提示 */ }
}

async function loadSettings() {
  try {
    const s = await api.getSettings()
    thresholdDraft.value = s.low_balance_threshold ?? 1
  } catch { /* 已提示 */ }
}

async function saveSettings() {
  savingThreshold.value = true
  try {
    const v = Number(thresholdDraft.value) || 0
    await api.updateSettings({ low_balance_threshold: v })
    thresholdDraft.value = v
    toast('告警阈值已保存', 'success')
  } catch { /* 已提示 */ }
  finally { savingThreshold.value = false }
}

// ===== 运行配置 =====
const config = ref(null)
const configLoading = ref(false)

async function loadConfig() {
  configLoading.value = true
  try {
    config.value = await api.config()
  } catch { /* 已提示 */ }
  finally { configLoading.value = false }
}

const cfgRows = computed(() => {
  const c = config.value || {}
  return [
    { label: '存活探测间隔', value: c.checker?.alive_interval != null ? `${c.checker.alive_interval} 秒` : '—' },
    { label: '价格同步间隔', value: c.checker?.pricing_interval != null ? `${c.checker.pricing_interval} 分钟` : '—' },
    { label: '推理探针间隔', value: c.checker?.probe_interval != null ? `${c.checker.probe_interval} 小时` : '—' },
    { label: '每日探针预算', value: c.checker?.daily_probe_budget != null ? `$${c.checker.daily_probe_budget}` : '—' },
    { label: '默认路由策略', value: c.routing?.default_strategy || '—' },
    { label: '最大重试次数', value: c.routing?.max_attempts ?? '—' },
    { label: '总预算耗时', value: c.routing?.total_budget_ms != null ? `${c.routing.total_budget_ms} ms` : '—' },
    { label: '价格上限（倍率）', value: c.routing?.max_price_cap != null ? `×${c.routing.max_price_cap}` : '—' },
    { label: '熔断失败率阈值', value: c.routing?.open_failure_rate != null ? `${Math.round(c.routing.open_failure_rate * 100)}%` : '—' },
  ]
})

onMounted(() => { loadKeys(); loadConfig(); loadGroups(); loadChannels(); loadSettings(); loadModelPrices() })
</script>

<template>
  <div class="page-wrap fade-in">
    <div class="page-head">
      <div>
        <div class="page-title">设置</div>
        <div class="page-sub">连接、访问凭证与运行配置</div>
      </div>
    </div>

    <div class="settings-grid">
      <!-- 连接 -->
      <div class="card card-pad">
        <div class="set-title">连接</div>
        <p class="set-desc">前端如何找到 Gateway。留空表示同源（开发经 Vite 代理，生产由 Gateway 托管）。</p>
        <div class="field">
          <label class="field-label">Gateway 地址</label>
          <input v-model="baseDraft" class="input mono" placeholder="http://localhost:8080">
        </div>
        <div class="field">
          <label class="field-label">API Key（Bearer Token）</label>
          <input v-model="keyDraft" type="password" class="input mono" :placeholder="isDev ? 'test-admin-key' : '请输入管理员 API Key'" @keyup.enter="saveConn">
          <div v-if="isDev" class="field-hint">本地开发默认 Key：<span class="mono">test-admin-key</span>（仅 config.local.yaml 开启引导时自动创建；生产环境使用启动日志中生成的一次性管理员 Key）</div>
        </div>
        <div class="row gap-2">
          <button class="btn btn-ghost" @click="testConn" :disabled="testing"><Icon name="plug" :size="14" />{{ testing ? '测试中…' : '测试连接' }}</button>
          <button class="btn btn-primary" @click="saveConn">保存</button>
          <span v-if="connTest" :class="connTest.ok ? 'text-green' : 'text-red'" style="font-size:12.5px">
            {{ connTest.ok ? '✓ ' + connTest.msg : '✗ ' + connTest.msg }}
          </span>
        </div>
      </div>

      <!-- API Keys -->
      <div class="card card-pad">
        <div class="row">
          <div class="set-title">API Keys</div>
          <span class="spacer" />
          <button class="btn btn-primary btn-sm" @click="showCreateKey = true; newKeyResult = null; newKeyGroups = []"><Icon name="plus" :size="13" />创建 Key</button>
        </div>
        <p class="set-desc">管理调用方访问凭证。明文仅在创建时展示一次。</p>

        <div v-if="keysLoading" class="skeleton" style="height:80px" />
        <EmptyState v-else-if="keys.length === 0" icon="key" title="暂无 API Keys" desc="创建第一个调用凭证" style="padding:26px 0" />
        <div v-for="k in keys" :key="k.id" class="row gap-3 key-row">
          <div class="key-ico"><Icon name="key" :size="16" /></div>
          <div class="grow">
            <div class="row gap-2">
              <span class="mono" style="font-weight:600;font-size:13px">{{ k.prefix }}••••••••</span>
              <span class="badge" :class="k.role === 'admin' ? 'badge-red' : 'badge-blue'">{{ k.role }}</span>
              <span class="badge" :class="k.enabled ? 'badge-green' : 'badge-gray'">{{ k.enabled ? '启用' : '禁用' }}</span>
            </div>
            <div class="row gap-2 mt-1">
              <span class="text-3" style="font-size:11.5px">创建于 {{ fmtDate(k.created_at) }}</span>
              <template v-if="k.role === 'caller'">
                <span v-if="(k.groups || []).length" class="row gap-1">
                  <span v-for="g in k.groups" :key="g.id" class="badge badge-teal" style="font-size:10px">{{ g.name }}</span>
                </span>
                <span v-else class="badge badge-gray" style="font-size:10px">不限制分组</span>
              </template>
            </div>
          </div>
          <button v-if="k.role === 'caller'" class="btn btn-ghost btn-sm" @click="openBindGroups(k)"><Icon name="layers" :size="13" />分组</button>
          <button class="btn btn-ghost btn-sm" @click="toggleKey(k)">{{ k.enabled ? '禁用' : '启用' }}</button>
          <button class="btn btn-danger btn-sm" @click="askRevokeKey(k)">撤销</button>
        </div>
      </div>

      <!-- 运行配置 -->
      <div class="card card-pad" style="grid-column:1/-1">
        <div class="set-title">运行配置</div>
        <p class="set-desc">从 Gateway 实时读取（只读）。修改请编辑 config.yaml 后重启服务。</p>
        <div class="cfg-grid">
          <div v-for="row in cfgRows" :key="row.label" class="cfg-row">
            <span class="cfg-label">{{ row.label }}</span>
            <span class="cfg-val mono">{{ row.value }}</span>
          </div>
        </div>
      </div>

      <!-- 官方模型价格 -->
      <div class="card card-pad" style="grid-column:1/-1">
        <div class="row">
          <div class="set-title">官方模型价格</div>
          <span class="spacer" />
          <button class="btn btn-primary btn-sm" @click="openPriceModal()"><Icon name="plus" :size="13" />添加模型价格</button>
        </div>
        <p class="set-desc">倍率实测的官方基准价（$/1M，输入/输出分开）。价格会随官方调价变化，请以官网为准；修改后路由价格估算立即生效。</p>
        <div v-if="pricesLoading" class="skeleton" style="height:80px" />
        <EmptyState v-else-if="modelPrices.length === 0" icon="gauge" title="价格库为空" desc="添加模型官方价格后，实测倍率将按官网价精确换算" style="padding:26px 0" />
        <div v-else class="table-wrap">
          <table>
            <thead><tr><th scope="col">模型</th><th scope="col">输入价 $/1M</th><th scope="col">输出价 $/1M</th><th scope="col">缓存读 $/1M</th><th scope="col">缓存写 $/1M</th><th scope="col">备注</th><th scope="col" style="width:110px"><span class="sr-only">操作</span></th></tr></thead>
            <tbody>
              <tr v-for="p in modelPrices" :key="p.model">
                <td data-label="模型"><span class="badge badge-blue mono">{{ p.model }}</span></td>
                <td class="mono" data-label="输入价">${{ Number(p.input_price_per_m).toFixed(4) }}</td>
                <td class="mono" data-label="输出价">${{ Number(p.output_price_per_m).toFixed(4) }}</td>
                <td class="mono text-3" data-label="缓存读">{{ p.cached_read_per_m != null ? '$' + Number(p.cached_read_per_m).toFixed(4) : '—' }}</td>
                <td class="mono text-3" data-label="缓存写">{{ p.cached_write_per_m != null ? '$' + Number(p.cached_write_per_m).toFixed(4) : '—' }}</td>
                <td class="text-3" style="font-size:12px" :title="p.note" data-label="备注">{{ p.note || '—' }}</td>
                <td class="row gap-1" style="justify-content:flex-end">
                  <button class="btn btn-ghost btn-sm" @click="openPriceModal(p)"><Icon name="pencil" :size="12" /></button>
                  <button class="btn btn-ghost btn-sm" :aria-label="'删除 ' + p.model" @click="askRemovePrice(p)"><Icon name="trash" :size="12" /></button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 测试台设置 -->
      <div class="card card-pad" style="grid-column:1/-1">
        <div class="set-title">请求测试台设置</div>
        <p class="set-desc">每个中转站站点都有专属的默认测试模型：在测试台选择站点后自动预填该模型，也可从该站点已映射的模型中任选。留空则回退到该站点模型映射的第一个模型。</p>
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th scope="col" style="width:180px">站点</th><th scope="col">默认测试模型</th><th scope="col" style="width:90px"><span class="sr-only">操作</span></th></tr>
            </thead>
            <tbody>
              <tr v-for="ch in channels" :key="ch.id">
                <td data-label="站点"><span class="badge badge-teal">{{ ch.name }}</span></td>
                <td data-label="默认测试模型">
                  <input v-model="testModelDrafts[ch.id]" class="input mono" :list="'tm-' + ch.id"
                    :placeholder="(ch.model_mapping && Object.keys(ch.model_mapping)[0]) || '如 gpt-4o'">
                  <datalist :id="'tm-' + ch.id">
                    <option v-for="m in Object.keys(ch.model_mapping || {})" :key="m" :value="m" />
                  </datalist>
                </td>
                <td>
                  <button class="btn btn-ghost btn-sm" @click="saveChannelTestModel(ch)" :disabled="savingTestModel === ch.id">
                    {{ savingTestModel === ch.id ? '保存中…' : '保存' }}
                  </button>
                </td>
              </tr>
              <tr v-if="!channels.length"><td colspan="3" class="text-3" style="padding:14px">暂无站点，请先在「站点」页添加</td></tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 告警设置 -->
      <div class="card card-pad">
        <div class="set-title">告警设置</div>
        <p class="set-desc">上游余额低于阈值时产生告警（显示在总览页告警区与侧边栏红点）。</p>
        <div class="field">
          <label class="field-label">低余额告警阈值（$）</label>
          <div class="row gap-2">
            <input v-model.number="thresholdDraft" type="number" min="0" step="0.1" class="input" style="width:140px">
            <button class="btn btn-primary" @click="saveSettings" :disabled="savingThreshold">{{ savingThreshold ? '保存中…' : '保存' }}</button>
          </div>
          <div class="field-hint">余额检测由 checker 进程每 10 分钟自动执行一次（分组可在「站点 → 管理分组」中覆盖间隔）。</div>
        </div>
      </div>
    </div>

    <!-- 创建 Key 弹窗 -->
    <BaseModal v-if="showCreateKey" title="创建 API Key" width="420px" @close="showCreateKey = false">
      <template v-if="!newKeyResult">
        <div class="field">
          <label class="field-label">角色</label>
          <div class="row gap-2">
            <button class="seg2" :class="{ on: newKeyRole === 'caller' }" @click="newKeyRole = 'caller'">caller · 仅调用 API</button>
            <button class="seg2" :class="{ on: newKeyRole === 'admin' }" @click="newKeyRole = 'admin'">admin · 完全权限</button>
          </div>
        </div>
        <div v-if="newKeyRole === 'caller'" class="field">
          <label class="field-label">绑定分组（不选 = 不限制，可选全部）</label>
          <div class="row gap-2" style="flex-wrap:wrap">
            <button v-for="g in groups" :key="g.id" type="button"
              class="badge" :class="newKeyGroups.includes(g.id) ? 'badge-teal' : 'badge-gray'"
              style="cursor:pointer;border:none;font-family:inherit"
              @click="newKeyGroups.includes(g.id) ? newKeyGroups.splice(newKeyGroups.indexOf(g.id), 1) : newKeyGroups.push(g.id)">
              {{ g.name }}
            </button>
            <span v-if="!groups.length" class="text-3" style="font-size:12px">暂无分组</span>
          </div>
          <div class="field-hint">绑定后，此 Key 的请求将被限定在所选分组的站点内路由。</div>
        </div>
      </template>
      <template v-else>
        <div class="key-once">
          <Icon name="alert" :size="15" style="color:var(--orange)" />
          <span style="font-weight:600">请立即保存，此 Key 只展示这一次</span>
        </div>
        <div class="code mt-2" style="user-select:all;color:var(--blue);font-size:13px">{{ newKeyResult }}</div>
      </template>
      <template #footer>
        <template v-if="!newKeyResult">
          <button class="btn btn-ghost" @click="showCreateKey = false">取消</button>
          <button class="btn btn-primary" @click="createKey" :disabled="creating">{{ creating ? '生成中…' : '生成 Key' }}</button>
        </template>
        <template v-else>
          <button class="btn btn-ghost" @click="navigator.clipboard.writeText(newKeyResult); toast('已复制', 'success')"><Icon name="copy" :size="13" />复制</button>
          <button class="btn btn-primary" @click="showCreateKey = false">完成</button>
        </template>
      </template>
    </BaseModal>

    <!-- 绑定分组弹窗 -->
    <BaseModal v-if="showBindGroups" :title="`绑定分组 · ${bindingKey?.prefix}••••`" width="440px" @close="showBindGroups = false">
      <div class="field">
        <label class="field-label">该 Key 可使用的分组（不选 = 不限制，可用全部）</label>
        <div class="row gap-2" style="flex-wrap:wrap">
          <button v-for="g in groups" :key="g.id" type="button"
            class="badge" :class="bindingGroups.includes(g.id) ? 'badge-teal' : 'badge-gray'"
            style="cursor:pointer;border:none;font-family:inherit"
            @click="bindingGroups.includes(g.id) ? bindingGroups.splice(bindingGroups.indexOf(g.id), 1) : bindingGroups.push(g.id)">
            {{ g.name }}
          </button>
          <span v-if="!groups.length" class="text-3" style="font-size:12px">暂无分组，请先在「站点」页创建</span>
        </div>
        <div class="field-hint mt-2">请求未指定分组时，将自动限定在所选分组的站点并集内路由；显式指定未绑定分组将被拒绝（403）。</div>
      </div>
      <template #footer>
        <button class="btn btn-ghost" @click="showBindGroups = false">取消</button>
        <button class="btn btn-primary" @click="saveBinding" :disabled="savingBinding">{{ savingBinding ? '保存中…' : '保存' }}</button>
      </template>
    </BaseModal>

    <!-- 撤销 Key 确认 -->
    <ConfirmDialog
      v-if="confirmRevokeKey"
      title="撤销 API Key"
      :message="`确认撤销 ${confirmRevokeKey.prefix}•••• ？此操作不可恢复。`"
      confirm-text="撤销"
      danger
      @confirm="doRevokeKey"
      @cancel="confirmRevokeKey = null"
    />

    <!-- 删除价格确认 -->
    <ConfirmDialog
      v-if="confirmDeletePrice"
      title="删除官方价格"
      :message="`确认删除「${confirmDeletePrice.model}」的官方价格？`"
      confirm-text="删除"
      danger
      @confirm="doRemovePrice"
      @cancel="confirmDeletePrice = null"
    />

    <!-- 官方模型价格弹窗 -->
    <BaseModal v-if="showPriceModal" :title="editingPrice ? '编辑官方价格' : '添加官方价格'" width="440px" @close="showPriceModal = false">
      <div class="field">
        <label class="field-label">模型名 *</label>
        <input v-model="priceForm.model" class="input mono" :disabled="!!editingPrice" placeholder="如 gpt-5.5">
        <div v-if="editingPrice" class="field-hint">模型名不可修改；需要改请删除后重新添加。</div>
      </div>
      <div class="form-grid-2">
        <div class="field"><label class="field-label">官方输入价（$/1M）*</label><input v-model.number="priceForm.input_price_per_m" type="number" min="0" step="0.0001" class="input" placeholder="如 5"></div>
        <div class="field"><label class="field-label">官方输出价（$/1M）*</label><input v-model.number="priceForm.output_price_per_m" type="number" min="0" step="0.0001" class="input" placeholder="如 30"></div>
        <div class="field"><label class="field-label">缓存读价（$/1M，可选）</label><input v-model.number="priceForm.cached_read_per_m" type="number" min="0" step="0.0001" class="input" placeholder="留空 = 不支持/未知"></div>
        <div class="field"><label class="field-label">缓存写价（$/1M，可选）</label><input v-model.number="priceForm.cached_write_per_m" type="number" min="0" step="0.0001" class="input" placeholder="留空 = 不支持/按小时计费"></div>
      </div>
      <div class="field">
        <label class="field-label">备注（可选）</label>
        <input v-model="priceForm.note" class="input" placeholder="如：OpenAI 官方 2026-08">
      </div>
      <template #footer>
        <button class="btn btn-ghost" @click="showPriceModal = false">取消</button>
        <button class="btn btn-primary" @click="savePrice" :disabled="savingPrice">{{ savingPrice ? '保存中…' : '保存' }}</button>
      </template>
    </BaseModal>
  </div>
</template>

<style scoped>
.settings-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; align-items: start; }
.set-title { font-size: 16px; font-weight: 700; margin-bottom: 4px; }
.set-desc { font-size: 12.5px; color: var(--text-3); margin-bottom: 16px; line-height: 1.6; }

.key-row { padding: 11px 0; border-bottom: 1px solid var(--border); }
.key-row:last-child { border-bottom: none; }
.key-ico {
  width: 34px; height: 34px; border-radius: var(--radius-md);
  display: flex; align-items: center; justify-content: center; flex-shrink: 0;
  background: var(--blue-soft); color: var(--blue);
}

.cfg-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 4px 28px; }
.cfg-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 9px 0; border-bottom: 1px solid var(--border); }
.cfg-label { font-size: 13px; color: var(--text-2); }
.cfg-val { font-size: 12.5px; color: var(--text-1); font-weight: 600; }

.seg2 {
  flex: 1; padding: 9px 12px; border-radius: var(--radius-md);
  border: 1px solid var(--border-strong); background: var(--surface-solid);
  font-size: 12.5px; font-weight: 500; font-family: inherit; color: var(--text-2);
  cursor: pointer; transition: all var(--dur) var(--ease);
}
.seg2.on { border-color: var(--blue); color: var(--blue); background: var(--blue-soft); }

.key-once {
  display: flex; align-items: center; gap: 8px;
  padding: 10px 14px; border-radius: var(--radius-md);
  background: var(--orange-soft); color: var(--orange); font-size: 12.5px;
}

@media (max-width: 900px) {
  .settings-grid { grid-template-columns: 1fr; }
  .cfg-grid { grid-template-columns: 1fr 1fr; }
}
</style>
