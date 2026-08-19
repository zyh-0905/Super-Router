<script setup>
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '../api'
import { store, toast } from '../store'
import EmptyState from '../components/EmptyState.vue'
import SelectBox from '../components/SelectBox.vue'
import Icon from '../components/Icon.vue'
import { fmtMs } from '../utils'

const router = useRouter()

const req = ref({
  model: '',
  stream: false,
  group: '', // 分组名（空 = 不限定）
  max_tokens: 1024,
  temperature: 0.7,
  messages: [{ role: 'user', content: '' }],
})

const groups = ref([])
const channels = ref([])
const selChannelId = ref(null)
const selGroupId = ref(null) // 选中的分组 ID（过滤站点列表用）

async function loadGroups() {
  try { groups.value = (await api.listGroups()).groups || [] } catch { /* 忽略 */ }
}

async function loadChannels() {
  try { channels.value = (await api.listChannels()).channels || [] } catch { /* 忽略 */ }
}
onMounted(() => { loadGroups(); loadChannels() })


const selectedChannel = computed(() => channels.value.find(c => c.id === selChannelId.value) || null)

// 站点列表按选中分组过滤：未选分组 = 全部站点；选定分组 = 仅该分组内的站点
const filteredChannels = computed(() => {
  if (!selGroupId.value) return channels.value
  return channels.value.filter(c => (c.groups || []).some(g => g.id === selGroupId.value))
})

// 分组切换：同步请求参数（分组名）并校验当前选中站点是否仍在该分组内
function onGroupChange() {
  const g = groups.value.find(x => x.id === selGroupId.value)
  req.value.group = g ? g.name : ''
  if (selChannelId.value != null && !filteredChannels.value.some(c => c.id === selChannelId.value)) {
    selChannelId.value = null
    req.value.model = ''
  }
}

const channelOpts = computed(() => [
  { value: null, label: '不选择（手动填写模型）' },
  ...filteredChannels.value.map(c => ({ value: c.id, label: c.name })),
])
const groupOpts = computed(() => [
  { value: null, label: '不限定（全部站点）' },
  ...groups.value.map(g => ({ value: g.id, label: `${g.name}（${g.channel_count} 站点）` })),
])
const roleOpts = [
  { value: 'system', label: 'system' },
  { value: 'user', label: 'user' },
  { value: 'assistant', label: 'assistant' },
]

// 选中站点后：只加载该站点在设置中配置的默认测试模型（未配置则回退其映射的第一个模型）
function onChannelChange() {
  const ch = selectedChannel.value
  if (!ch) return
  req.value.model = ch.test_model || Object.keys(ch.model_mapping || {})[0] || ''
}

// 模型下拉提示：仅当前选中站点的已映射模型（未选站点时不提示）
const modelSuggestions = computed(() => {
  const ch = selectedChannel.value
  return ch ? Object.keys(ch.model_mapping || {}) : []
})

const loading = ref(false)
const streamText = ref('')
const result = ref(null) // { status, meta, body, displayText, raw, ttftMs, totalMs }
const tab = ref('response')
const autoScroll = ref(true) // TODO: 实现自动滚动到底部

// 等待动画：实时计时 + 阶段文案（首字节前「路由中」，首字节后「生成中」）
const elapsedMs = ref(0)
const waitPhase = ref('正在路由请求…')
let waitTimer = null

const statusCls = computed(() => {
  if (!result.value) return ''
  const s = result.value.status
  if (s >= 200 && s < 300) return 'badge-green'
  return 'badge-red'
})

function addMessage() { req.value.messages.push({ role: 'user', content: '' }) }
function removeMessage(i) { if (req.value.messages.length > 1) req.value.messages.splice(i, 1) }

async function send() {
  const msgs = req.value.messages.filter(m => m.content.trim() !== '')
  if (!msgs.length) { toast('请输入消息内容', 'error'); return }
  if (!req.value.model) { toast('请填写模型名', 'error'); return }

  loading.value = true
  streamText.value = ''
  result.value = null
  tab.value = 'response'
  waitPhase.value = '正在路由请求…'
  const t0 = performance.now()
  elapsedMs.value = 0
  waitTimer = setInterval(() => { elapsedMs.value = Math.round(performance.now() - t0) }, 100)

  try {
    const r = await api.chatCompletion({
      model: req.value.model,
      messages: msgs,
      max_tokens: req.value.max_tokens,
      temperature: req.value.temperature,
      stream: req.value.stream,
      group: req.value.group || undefined,
    }, {
      onDelta: (delta) => {
        if (result.value?.ttftMs == null) {
          result.value = { ...(result.value || {}), ttftMs: Math.round(performance.now() - t0) }
          waitPhase.value = '正在生成…'
        }
        streamText.value += delta
      },
    })

    if (r.stream) {
      result.value = {
        status: 200,
        meta: r.meta,
        ttftMs: result.value?.ttftMs ?? 0,
        totalMs: Math.round(performance.now() - t0),
        displayText: streamText.value,
        raw: streamText.value,
      }
    } else {
      const data = r.data
      const text = data.choices?.[0]?.message?.content || JSON.stringify(data, null, 2)
      result.value = {
        status: 200,
        meta: r.meta,
        ttftMs: Math.round(performance.now() - t0),
        totalMs: Math.round(performance.now() - t0),
        displayText: text,
        body: data,
        raw: JSON.stringify(data, null, 2),
      }
    }
  } catch (e) {
    result.value = {
      status: e.status || 0,
      meta: e.meta || {},
      ttftMs: 0,
      totalMs: Math.round(performance.now() - t0),
      displayText: e.message,
      raw: e.message,
    }
  } finally {
    clearInterval(waitTimer)
    waitTimer = null
    loading.value = false
  }
}

function copyCurl() {
  const msgs = req.value.messages.filter(m => m.content.trim() !== '')
  const body = JSON.stringify({ model: req.value.model, messages: msgs, stream: req.value.stream })
  const curl = `curl -X POST ${store.baseURL || location.origin}/v1/chat/completions \\\n  -H "Authorization: Bearer ${store.apiKey}" \\\n  -H "Content-Type: application/json" \\\n  -d '${body}'`
  navigator.clipboard.writeText(curl)
  toast('cURL 已复制', 'success')
}

function openDecision() {
  const rid = result.value?.meta?.requestId
  if (!rid) { toast('本次请求未返回 Request ID', 'error'); return }
  router.push({ path: '/decisions', query: { id: rid } })
}

function reset() {
  result.value = null
  streamText.value = ''
  tab.value = 'response'
}
</script>

<template>
  <div class="page-wrap fade-in">
    <div class="page-head">
      <div>
        <div class="page-title">测试台</div>
        <div class="page-sub">向网关发送真实的 Chat Completions 请求，验证路由与上游</div>
      </div>
      <button class="btn btn-ghost" @click="copyCurl"><Icon name="copy" :size="15" />复制 cURL</button>
    </div>

    <div class="play-grid">
      <!-- 左：请求构建器 -->
      <div class="card play-pane">
        <div class="card-head">请求<Icon name="terminal" :size="15" style="color:var(--text-3);margin-left:2px" /></div>
        <div class="play-body">
          <div class="form-grid-2">
            <div class="field">
              <label class="field-label">站点（自动填入该站点默认测试模型）</label>
              <SelectBox v-model="selChannelId" :options="channelOpts" @change="onChannelChange" />
              <div class="field-hint" style="margin-top:4px">
                {{ selGroupId ? `仅显示分组内站点（${filteredChannels.length} 个）` : `全部站点（${filteredChannels.length} 个）` }}
              </div>
            </div>
            <div class="field">
              <label class="field-label">分组（限定路由范围）</label>
              <SelectBox v-model="selGroupId" :options="groupOpts" @change="onGroupChange" />
              <div class="field-hint" style="margin-top:4px">选择分组后，上方站点列表只加载该分组内的站点</div>
            </div>
          </div>

          <div class="field">
            <label class="field-label">模型</label>
            <input v-model="req.model" class="input mono" list="known-models" :placeholder="selChannelId ? '自动填入该站点默认测试模型' : '如 gpt-4o / claude-sonnet-5'">
            <datalist id="known-models">
              <option v-for="m in modelSuggestions" :key="m" :value="m" />
            </datalist>
          </div>
          <div class="field-hint" style="margin-top:-8px;margin-bottom:8px">选择站点后自动填入该站点在设置页配置的默认测试模型，模型下拉只提示该站点的已映射模型。站点仅用于预填，实际路由仍由决策引擎决定。</div>

          <div class="field">
            <label class="field-label">消息</label>
            <div v-for="(msg, i) in req.messages" :key="i" class="msg-box">
              <div class="row gap-2 msg-head">
                <SelectBox v-model="msg.role" :options="roleOpts" size="sm" width="112px" />
                <span class="spacer" />
                <button v-if="req.messages.length > 1" class="icon-btn" style="width:26px;height:26px" @click="removeMessage(i)"><Icon name="x" :size="13" /></button>
              </div>
              <textarea v-model="msg.content" class="textarea" style="border:none;border-radius:0;min-height:64px" :placeholder="msg.role === 'user' ? '输入用户消息…' : '输入消息内容…'" />
            </div>
            <button class="btn btn-ghost btn-sm mt-1" @click="addMessage"><Icon name="plus" :size="13" />添加消息</button>
          </div>

          <div class="field">
            <label class="field-label">响应模式</label>
            <div class="row gap-2">
              <button class="seg" :class="{ on: !req.stream }" @click="req.stream = false">标准</button>
              <button class="seg" :class="{ on: req.stream }" @click="req.stream = true">流式（逐字输出）</button>
            </div>
          </div>

          <div class="form-grid-2">
            <div class="field"><label class="field-label">max_tokens</label><input v-model.number="req.max_tokens" type="number" min="1" class="input"></div>
            <div class="field"><label class="field-label">temperature</label><input v-model.number="req.temperature" type="number" step="0.1" min="0" max="2" class="input"></div>
          </div>

          <button class="btn btn-primary btn-lg w-full" style="width:100%;justify-content:center" @click="send" :disabled="loading">
            <Icon :name="loading ? 'refresh' : 'arrow_right'" :size="16" :class="{ 'spin': loading }" />
            {{ loading ? '请求中…' : '发送请求' }}
          </button>
        </div>
      </div>

      <!-- 右：响应 -->
      <div class="card play-pane">
        <div class="card-head">
          响应
          <div class="row gap-2" style="margin-left:auto">
            <button v-for="t in [{k:'response',l:'内容'},{k:'decision',l:'路由'},{k:'raw',l:'原始'}]" :key="t.k"
              class="seg" :class="{ on: tab === t.k }" @click="tab = t.k" style="font-size:11.5px;padding:4px 12px">
              {{ t.l }}
            </button>
          </div>
        </div>
        <div class="play-body">
          <!-- 状态条 -->
          <div v-if="result" class="row gap-3 resp-status">
            <span class="badge" :class="statusCls">{{ result.status || '✗' }}</span>
            <span class="text-3 mono" style="font-size:11.5px">{{ fmtMs(result.totalMs) }} 总耗时</span>
            <span class="text-3 mono" style="font-size:11.5px">首字节 {{ fmtMs(result.ttftMs) }}</span>
            <span class="spacer" />
            <span v-if="result.meta?.group" class="badge badge-teal">{{ result.meta.group }}</span>
            <span class="badge badge-blue">{{ result.meta?.channel || '—' }}</span>
            <span v-if="result.meta?.strategy" class="badge badge-purple">{{ result.meta.strategy }}</span>
          </div>

          <!-- 请求进行中状态条 -->
          <div v-if="loading && !result" class="row gap-3 resp-status waiting-bar">
            <span class="wait-dot" />
            <span class="wait-label">{{ waitPhase }}</span>
            <span class="mono wait-timer">{{ (elapsedMs / 1000).toFixed(1) }}s</span>
          </div>

          <!-- 内容 -->
          <div v-if="tab === 'response'" class="resp-text" :class="{ streaming: loading }">
            <template v-if="loading && streamText">
              {{ streamText }}<span class="cursor" />
            </template>
            <!-- 首字节前的等待动画 -->
            <div v-else-if="loading" class="wait-anim">
              <div class="dots"><span v-for="i in 3" :key="i" :style="{ animationDelay: `${(i - 1) * 0.18}s` }" /></div>
              <div class="wait-line"><span class="wait-line-dot" />{{ waitPhase }}</div>
              <div class="wait-sub mono">{{ (elapsedMs / 1000).toFixed(1) }}s · 正在等待上游响应</div>
            </div>
            <template v-else-if="result">{{ result.displayText }}</template>
            <EmptyState v-else icon="terminal" title="等待请求" desc="构建左侧请求后点击「发送请求」" style="padding:40px 0" />
          </div>

          <!-- 路由决策 -->
          <div v-else-if="tab === 'decision'">
            <div v-if="loading" class="wait-anim" style="padding:36px 0">
              <div class="dots"><span v-for="i in 3" :key="i" :style="{ animationDelay: `${(i - 1) * 0.18}s` }" /></div>
              <div class="wait-line"><span class="wait-line-dot" />{{ waitPhase }}</div>
              <div class="wait-sub mono">请求完成前路由信息暂不可用</div>
            </div>
            <EmptyState v-else-if="!result" icon="bolt" title="暂无路由信息" desc="发送请求后展示网关实时路由结果" style="padding:40px 0" />
            <div v-else>
              <div class="form-grid-2">
                <div class="field"><label class="field-label">选中渠道</label><div class="code">{{ result.meta?.channel || '—' }}</div></div>
                <div class="field"><label class="field-label">路由策略</label><div class="code">{{ result.meta?.strategy || '—' }}</div></div>
                <div class="field"><label class="field-label">分组</label><div class="code">{{ result.meta?.group || '—' }}</div></div>
                <div class="field"><label class="field-label">渠道 ID</label><div class="code">{{ result.meta?.channelId || '—' }}</div></div>
                <div class="field" style="grid-column:1/-1"><label class="field-label">Trace ID</label><div class="code" style="font-size:11px">{{ result.meta?.traceId || '—' }}</div></div>
              </div>
              <div class="field-hint mb-3">以上信息来自网关响应头（X-Selected-Channel / X-Strategy / X-Group / X-Trace-ID），由路由决策引擎实时产生。</div>
              <button class="btn btn-ghost btn-sm" @click="openDecision"><Icon name="list" :size="13" />在决策日志中查看</button>
            </div>
          </div>

          <!-- 原始 -->
          <div v-else>
            <div v-if="loading && !result" class="wait-anim" style="padding:36px 0">
              <div class="dots"><span v-for="i in 3" :key="i" :style="{ animationDelay: `${(i - 1) * 0.18}s` }" /></div>
              <div class="wait-line"><span class="wait-line-dot" />{{ waitPhase }}</div>
              <div class="wait-sub mono">响应原文将在请求完成后展示</div>
            </div>
            <div v-else class="code" style="max-height:100%;overflow-y:auto">{{ result?.raw || '—' }}</div>
          </div>
        </div>
        <div v-if="result" class="play-foot">
          <button class="btn btn-ghost btn-sm" @click="reset"><Icon name="refresh" :size="13" />清空</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.play-grid { display: grid; grid-template-columns: 1fr 1.25fr; gap: 16px; min-height: 540px; }
.play-pane { display: flex; flex-direction: column; overflow: hidden; }
.play-body { flex: 1; overflow-y: auto; padding: 18px 22px; }
.play-foot { padding: 10px 22px; border-top: 1px solid var(--border); display: flex; justify-content: flex-end; }

.msg-box { border: 1px solid var(--border); border-radius: var(--radius-md); overflow: hidden; margin-bottom: 10px; background: var(--surface-solid); }
.msg-head { padding: 6px 10px; background: var(--surface); border-bottom: 1px solid var(--border); }

/* seg 样式已移至 base.css 全局 */

.resp-status { padding: 10px 14px; background: var(--surface-solid); border: 1px solid var(--border); border-radius: var(--radius-md); margin-bottom: 14px; flex-wrap: wrap; }

.resp-text {
  font-family: var(--font-mono); font-size: 12.5px; line-height: 1.8;
  white-space: pre-wrap; word-break: break-all; color: var(--text-1);
}
.cursor { display: inline-block; width: 7px; height: 14px; background: var(--blue); vertical-align: -2px; margin-left: 2px; animation: blink 0.8s step-end infinite; }

/* ===== 响应等待动画（请求进行中） ===== */
/* 顶部进行中状态条 */
.waiting-bar { background: var(--blue-soft); border-color: transparent; }
.wait-dot {
  width: 8px; height: 8px; border-radius: 50%; background: var(--blue); flex-shrink: 0;
  animation: waitPulse 1.6s ease-in-out infinite;
}
.wait-label { font-size: 12.5px; color: var(--text-2); }
.wait-timer { font-size: 11.5px; color: var(--text-3); font-variant-numeric: tabular-nums; }

/* 居中等待动画：iMessage 三点错峰弹跳 + 呼吸文案 */
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
/* 用户偏好减少动态时：静止圆点 + 纯透明度呼吸（不位移） */
@media (prefers-reduced-motion: reduce) {
  .wait-anim .dots span { animation-name: waitPulse; }
  .wait-dot, .wait-anim .wait-line-dot { animation: none; opacity: 0.8; }
}
@keyframes blink { 0%, 100% { opacity: 1; } 50% { opacity: 0; } }
.spin { animation: spin 1s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }

@media (max-width: 1000px) {
  .play-grid { grid-template-columns: 1fr; min-height: auto; }
  .play-pane { min-height: 380px; }
}
@media (max-width: 640px) {
  .play-pane { min-height: 320px; }
  .play-body { padding: 14px 16px; }
}
</style>
