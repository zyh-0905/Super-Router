<script setup>
// 站点直达测试面板（测试台 · 站点测试页签）：
// 绕过路由引擎，直达指定站点做两次真实推理（非流式 + 流式），
// 展示延迟/TTFT、余额差（所花余额）与实测倍率。
// 结果仅展示、不落库；每次测试可能产生少量上游费用。
import { ref, computed, onMounted } from 'vue'
import { api } from '../api'
import { toast } from '../store'
import Icon from './Icon.vue'
import SelectBox from './SelectBox.vue'
import EmptyState from './EmptyState.vue'
import { fmtMs, fmtNum } from '../utils'

const channels = ref([])
const selChannelId = ref(null)
const model = ref('')
const message = ref('hi')
const maxTokens = ref(128)

const loading = ref(false)
const result = ref(null)
const elapsedMs = ref(0)
let waitTimer = null

const selectedChannel = computed(() => channels.value.find(c => c.id === selChannelId.value) || null)
const modelSuggestions = computed(() => {
  const ch = selectedChannel.value
  return ch ? Object.keys(ch.model_mapping || {}) : []
})

async function loadChannels() {
  try { channels.value = (await api.listChannels()).channels || [] } catch { /* 已提示 */ }
}
onMounted(loadChannels)

// 选中站点后自动预填该站点默认测试模型（与分组测试页同款逻辑）
function onChannelChange() {
  const ch = selectedChannel.value
  if (!ch) return
  model.value = ch.test_model || Object.keys(ch.model_mapping || {})[0] || ''
}

async function run() {
  if (!selChannelId.value) { toast('请选择站点', 'error'); return }
  if (!model.value) { toast('请填写模型名', 'error'); return }
  if (!message.value.trim()) { toast('请输入消息内容', 'error'); return }

  loading.value = true
  result.value = null
  const t0 = performance.now()
  elapsedMs.value = 0
  waitTimer = setInterval(() => { elapsedMs.value = Math.round(performance.now() - t0) }, 100)

  try {
    result.value = await api.siteTest(selChannelId.value, {
      model: model.value,
      message: message.value,
      max_tokens: Number(maxTokens.value) || 128,
    })
  } catch (e) {
    toast(e.message || '站点测试失败', 'error')
  } finally {
    clearInterval(waitTimer)
    waitTimer = null
    loading.value = false
  }
}

// 分区状态徽章类
function sectionCls(section) {
  if (!section) return ''
  return section.ok ? 'badge-green' : 'badge-red'
}
function basisCls(basis) {
  return basis === 'official' ? 'badge-blue' : 'badge-gray'
}
function basisLabel(basis) {
  return basis === 'official' ? 'official（官网价）' : 'baseline（$10/1M 基准）'
}
const currencySymbol = (cur) => cur === 'CNY' ? '¥' : (cur === 'USD' ? '$' : cur + ' ')
</script>

<template>
  <div class="play-grid">
    <!-- 左：测试配置 -->
    <div class="card play-pane">
      <div class="card-head">站点测试<Icon name="target" :size="15" style="color:var(--text-3);margin-left:2px" /></div>
      <div class="play-body">
        <div class="field">
          <label class="field-label">站点（直达，绕过路由引擎）</label>
          <SelectBox v-model="selChannelId" :options="channels.map(c => ({ value: c.id, label: c.name }))" @change="onChannelChange" />
          <div class="field-hint" style="margin-top:4px">选择后自动填入该站点默认测试模型（test_model）</div>
        </div>
        <div class="field">
          <label class="field-label">模型</label>
          <input v-model="model" class="input mono" list="site-test-models" placeholder="自动填入站点默认测试模型">
          <datalist id="site-test-models">
            <option v-for="m in modelSuggestions" :key="m" :value="m" />
          </datalist>
        </div>
        <div class="field">
          <label class="field-label">消息</label>
          <textarea v-model="message" class="textarea" rows="3" placeholder="默认 hi，可自定义" />
        </div>
        <div class="field">
          <label class="field-label">max_tokens</label>
          <input v-model.number="maxTokens" type="number" min="1" max="512" class="input">
          <div class="field-hint" style="margin-top:4px">上限 512（流式与非流式各一次真实推理，可能产生少量上游费用）</div>
        </div>
        <button class="btn btn-primary btn-lg w-full" style="width:100%;justify-content:center" @click="run" :disabled="loading">
          <Icon :name="loading ? 'refresh' : 'arrow_right'" :size="16" :class="{ 'spin': loading }" />
          {{ loading ? '测试中…' : '开始测试' }}
        </button>
      </div>
    </div>

    <!-- 右：结果 -->
    <div class="card play-pane">
      <div class="card-head">结果
        <span class="spacer" />
        <span v-if="result" class="mono text-3" style="font-size:11.5px">{{ fmtMs(result.elapsed_ms) }} 总耗时</span>
      </div>
      <div class="play-body">
        <!-- 测试中等待动画 -->
        <div v-if="loading" class="wait-anim">
          <div class="dots"><span v-for="i in 3" :key="i" :style="{ animationDelay: `${(i - 1) * 0.18}s` }" /></div>
          <div class="wait-line"><span class="wait-line-dot" />正在执行：余额前 → 非流式 → 余额中 → 流式 → 余额后</div>
          <div class="wait-sub mono">{{ (elapsedMs / 1000).toFixed(1) }}s · 直达 {{ selectedChannel?.name || '—' }} 真实推理中</div>
        </div>

        <EmptyState v-else-if="!result" icon="target" title="等待测试" desc="选择站点后点击「开始测试」，直达站点验证真实链路" style="padding:40px 0" />

        <template v-else>
          <!-- 延迟指标卡 -->
          <div class="stat-grid" style="grid-template-columns:repeat(4,1fr);margin-bottom:14px">
            <div class="stat-card">
              <div class="stat-label">非流式 TTFT</div>
              <div class="stat-value mono">{{ result.non_stream.ok ? fmtMs(result.non_stream.ttfb_ms) : '—' }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">非流式总耗时</div>
              <div class="stat-value mono">{{ result.non_stream.ok ? fmtMs(result.non_stream.total_ms) : '—' }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">流式 TTFT</div>
              <div class="stat-value mono">{{ result.stream.ok ? fmtMs(result.stream.ttfb_ms) : '—' }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">流式总耗时</div>
              <div class="stat-value mono">{{ result.stream.ok ? fmtMs(result.stream.total_ms) : '—' }}</div>
            </div>
          </div>

          <!-- 余额差 -->
          <div class="card-pad sect">
            <div class="row gap-2">
              <span class="sect-title">所花余额</span>
              <span class="badge" :class="sectionCls(result.balance)">{{ result.balance.ok ? '正常' : '不可用' }}</span>
              <span class="spacer" />
              <span v-if="result.balance.ok" class="mono text-2" style="font-size:12.5px">
                {{ currencySymbol(result.balance.currency) }}{{ fmtNum(result.balance.cost_total) }} 合计
              </span>
            </div>
            <div v-if="result.balance.ok" class="form-grid-2 mt-2">
              <div class="field"><label class="field-label">测试前</label><div class="code">{{ currencySymbol(result.balance.currency) }}{{ fmtNum(result.balance.before) }}</div></div>
              <div class="field"><label class="field-label">非流式后</label><div class="code">{{ currencySymbol(result.balance.currency) }}{{ fmtNum(result.balance.mid) }}<span class="text-3">（-{{ fmtNum(result.balance.cost_non_stream) }}）</span></div></div>
              <div class="field"><label class="field-label">流式后</label><div class="code">{{ currencySymbol(result.balance.currency) }}{{ fmtNum(result.balance.after) }}<span class="text-3">（-{{ fmtNum(result.balance.cost_stream) }}）</span></div></div>
              <div class="field"><label class="field-label">货币</label><div class="code">{{ result.balance.currency }}</div></div>
            </div>
            <div v-if="!result.balance.ok && result.balance.error" class="field-hint text-red mt-1">{{ result.balance.error }}</div>
            <div v-if="result.balance.warning" class="field-hint text-orange mt-1">{{ result.balance.warning }}</div>
          </div>

          <!-- 倍率 -->
          <div class="card-pad sect">
            <div class="row gap-2">
              <span class="sect-title">实测倍率</span>
              <span class="badge" :class="sectionCls(result.ratio)">{{ result.ratio.ok ? '已计算' : '不可用' }}</span>
              <span class="spacer" />
              <span v-if="result.ratio.ok" class="mono text-2" style="font-size:13px">× {{ result.ratio.real_ratio }}</span>
              <span v-if="result.ratio.ok" class="badge" :class="basisCls(result.ratio.basis)">{{ basisLabel(result.ratio.basis) }}</span>
            </div>
            <div v-if="result.ratio.ok" class="form-grid-2 mt-2">
              <div class="field"><label class="field-label">官网输入价（$/1M）</label><div class="code">{{ result.ratio.official_input_per_m || '—' }}</div></div>
              <div class="field"><label class="field-label">官网输出价（$/1M）</label><div class="code">{{ result.ratio.official_output_per_m || '—' }}</div></div>
              <div class="field"><label class="field-label">推算输入单价（$/1M）</label><div class="code">{{ result.ratio.estimated_input_per_m || '—' }}</div></div>
              <div class="field"><label class="field-label">推算输出单价（$/1M）</label><div class="code">{{ result.ratio.estimated_output_per_m || '—' }}</div></div>
            </div>
            <div v-if="!result.ratio.ok && result.ratio.error" class="field-hint text-red mt-1">{{ result.ratio.error }}</div>
            <div v-if="result.ratio.warning" class="field-hint text-orange mt-1">{{ result.ratio.warning }}</div>
          </div>

          <!-- Token 用量 -->
          <div class="card-pad sect">
            <span class="sect-title">Token 用量</span>
            <div class="form-grid-2 mt-2">
              <div class="field">
                <label class="field-label">非流式</label>
                <div class="code" v-if="result.non_stream.usage_present">{{ result.non_stream.prompt_tokens }} 入 + {{ result.non_stream.completion_tokens }} 出 = {{ result.non_stream.total_tokens }}</div>
                <div class="code text-3" v-else>上游未返回 usage</div>
              </div>
              <div class="field">
                <label class="field-label">流式</label>
                <div class="code" v-if="result.stream.usage_present">{{ result.stream.prompt_tokens }} 入 + {{ result.stream.completion_tokens }} 出 = {{ result.stream.total_tokens }}</div>
                <div class="code text-3" v-else>上游未返回 usage</div>
              </div>
            </div>
          </div>

          <!-- 响应内容 -->
          <div class="card-pad sect" v-for="(sec, key) in { non_stream: '非流式响应', stream: '流式响应' }" :key="key">
            <div class="row gap-2">
              <span class="sect-title">{{ sec }}</span>
              <span class="badge" :class="sectionCls(result[key])">{{ result[key].ok ? '200' : '失败' }}</span>
              <span v-if="result[key].usage_present" class="text-3 mono" style="font-size:11px">上游模型 {{ result[key].actual_model || '—' }}</span>
              <span v-if="key === 'stream' && result[key].ok" class="text-3 mono" style="font-size:11px">{{ result[key].stream_events }} 事件{{ result[key].done_received ? ' · [DONE]' : '' }}</span>
            </div>
            <div v-if="!result[key].ok && result[key].error" class="field-hint text-red mt-1">{{ result[key].error }}</div>
            <details v-else-if="result[key].ok" class="mt-1">
              <summary class="sect-summary">展开响应文本</summary>
              <div class="code" style="max-height:220px;overflow-y:auto;white-space:pre-wrap">{{ result[key].text || '（空响应）' }}</div>
            </details>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
.play-grid { display: grid; grid-template-columns: 1fr 1.25fr; gap: 16px; min-height: 540px; }
.play-pane { display: flex; flex-direction: column; overflow: hidden; }
.play-body { flex: 1; overflow-y: auto; padding: 18px 22px; }

.sect { border: 1px solid var(--border); border-radius: var(--radius-md); background: var(--surface-solid); padding: 12px 14px; margin-bottom: 12px; }
.sect-title { font-size: 13px; font-weight: 600; color: var(--text-1); }
.sect-summary { cursor: pointer; font-size: 12px; color: var(--blue); user-select: none; }

/* 等待动画（与测试台分组测试页同款） */
.wait-anim {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 18px; padding: 72px 0 64px; text-align: center;
}
.wait-anim .dots { display: flex; gap: 9px; }
.wait-anim .dots span {
  width: 11px; height: 11px; border-radius: 50%; background: var(--blue);
  animation: dotBounce 1.15s ease-in-out infinite;
}
.wait-anim .wait-line { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-2); }
.wait-anim .wait-line-dot {
  width: 7px; height: 7px; border-radius: 50%; background: var(--blue); flex-shrink: 0;
  animation: waitPulse 1.6s ease-in-out infinite;
}
.wait-anim .wait-sub { font-size: 11.5px; color: var(--text-3); font-variant-numeric: tabular-nums; }

@keyframes dotBounce {
  0%, 55%, 100% { transform: translateY(0); opacity: 0.55; }
  25% { transform: translateY(-9px); opacity: 1; }
}
@keyframes waitPulse {
  0%, 100% { opacity: 0.25; }
  50% { opacity: 1; }
}
@media (prefers-reduced-motion: reduce) {
  .wait-anim .dots span { animation-name: waitPulse; }
  .wait-anim .wait-line-dot { animation: none; opacity: 0.8; }
}
.spin { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 1000px) {
  .play-grid { grid-template-columns: 1fr; min-height: auto; }
  .play-pane { min-height: 380px; }
}
</style>
