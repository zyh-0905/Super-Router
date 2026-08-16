<script setup>
// 右下角预警弹窗（通知卡片风格）：
//   - 不遮挡页面，从右下角以「弹跳」动效跳入；
//   - 最多同屏 3 条，超出显示「+N 条」；队列随时间自动消化；
//   - 严重度配色（critical 红 / warning 橙）+ 底部倒计时进度条；
//   - 自动消失（warning 15s / critical 30s），可手动关闭或跳转处理页。
import { computed, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { store, dismissAlertPopup, dismissAllAlertPopups } from '../store'
import Icon from './Icon.vue'

const router = useRouter()

const MAX_VISIBLE = 3
const visible = computed(() => store.alertPopups.slice(0, MAX_VISIBLE))
const hiddenCount = computed(() => Math.max(0, store.alertPopups.length - MAX_VISIBLE))

function ttlMs(sev) {
  return sev === 'critical' ? 30000 : 15000
}

// 告警 id 前缀 → 处理页面
function targetRoute(id) {
  if (!id) return '/'
  if (id.startsWith('cb_')) return '/circuit'
  if (id.startsWith('bal_') || id.startsWith('ratio_') || id.startsWith('dis_')) return '/channels'
  return '/'
}

function goHandle(p) {
  dismissAlertPopup(p.popupId)
  router.push(targetRoute(p.id))
}

// 每条弹窗的自动消失计时器（按 popupId 管理，防止重复/泄漏）
const timers = new Map()
const popupIds = computed(() => store.alertPopups.map(p => p.popupId))
watch(popupIds, (ids) => {
  for (const p of store.alertPopups) {
    if (!timers.has(p.popupId)) {
      timers.set(p.popupId, setTimeout(() => dismissAlertPopup(p.popupId), ttlMs(p.sev)))
    }
  }
  for (const [id, t] of timers) {
    if (!ids.includes(id)) {
      clearTimeout(t)
      timers.delete(id)
    }
  }
}, { immediate: true })

onUnmounted(() => {
  for (const t of timers.values()) clearTimeout(t)
  timers.clear()
})
</script>

<template>
  <Teleport to="body">
    <div class="alert-stack" :class="{ 'reduce-motion': false }">
      <TransitionGroup name="pop">
        <div v-for="p in visible" :key="p.popupId" class="alert-card" :class="'sev-' + p.sev" role="alert">
          <!-- 倒计时进度条 -->
          <div class="countdown" :style="{ animationDuration: ttlMs(p.sev) + 'ms' }" :aria-hidden="true" />

          <div class="card-head">
            <span class="alert-icon">
              <Icon name="alert" :size="16" />
            </span>
            <div class="grow">
              <div class="row gap-2">
                <span class="card-title">预警</span>
                <span class="sev-badge" :class="'sev-' + p.sev">
                  {{ p.sev === 'critical' ? '严重' : '警告' }}
                </span>
              </div>
              <div class="card-sub">
                {{ p.channel ? '渠道：' + p.channel : '系统' }} · {{ p.ago || '刚刚' }}
              </div>
            </div>
            <button class="icon-btn" style="width:24px;height:24px" @click="dismissAlertPopup(p.popupId)">
              <Icon name="x" :size="12" />
            </button>
          </div>

          <div class="card-body">{{ p.name }}</div>

          <div class="card-foot">
            <div class="spacer" />
            <button class="btn btn-ghost btn-sm" @click="dismissAlertPopup(p.popupId)">知道了</button>
            <button class="btn btn-primary btn-sm" @click="goHandle(p)">查看详情</button>
          </div>
        </div>
      </TransitionGroup>

      <!-- 队列溢出：一键清空 -->
      <button v-if="hiddenCount > 0" class="queue-pill" @click="dismissAllAlertPopups()">
        还有 {{ hiddenCount }} 条未读 · 全部忽略
      </button>
    </div>
  </Teleport>
</template>

<style scoped>
.alert-stack {
  position: fixed; right: 16px; bottom: 16px;
  z-index: var(--z-toast);
  display: flex; flex-direction: column; align-items: flex-end;
  gap: 10px;
  width: min(400px, calc(100vw - 32px));
  pointer-events: none;
}

.alert-card {
  position: relative;
  width: 100%;
  background: var(--surface-raised);
  backdrop-filter: saturate(180%) blur(28px);
  -webkit-backdrop-filter: saturate(180%) blur(28px);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: 0 12px 34px rgba(0, 0, 0, 0.18);
  overflow: hidden;
  pointer-events: all;
}
.alert-card.sev-critical { border-left: 3px solid var(--red); }
.alert-card.sev-warning { border-left: 3px solid #ff9f0a; }

/* 倒计时进度条（底部，随自动消失时间线性收缩） */
.countdown {
  position: absolute; left: 0; bottom: 0; height: 2.5px; width: 100%;
  transform-origin: left center;
  animation-name: shrink; animation-timing-function: linear; animation-fill-mode: forwards;
}
.sev-critical .countdown { background: linear-gradient(90deg, var(--red), rgba(255, 69, 58, 0.35)); }
.sev-warning .countdown { background: linear-gradient(90deg, #ff9f0a, rgba(255, 159, 10, 0.35)); }
@keyframes shrink { from { transform: scaleX(1); } to { transform: scaleX(0); } }

.card-head {
  display: flex; align-items: center; gap: 10px;
  padding: 12px 14px 8px;
}
.alert-icon {
  display: flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; border-radius: 9px; flex-shrink: 0;
}
.sev-critical .alert-icon { background: var(--red-soft); color: var(--red); }
.sev-warning .alert-icon { background: rgba(255, 159, 10, 0.12); color: #ff9f0a; }

.card-title { font-size: 13.5px; font-weight: 700; }
.card-sub { font-size: 11.5px; color: var(--text-3); margin-top: 1px; }

.sev-badge {
  font-size: 10.5px; font-weight: 700; padding: 1.5px 7px; border-radius: 999px;
}
.sev-badge.sev-critical { background: var(--red-soft); color: var(--red); }
.sev-badge.sev-warning { background: rgba(255, 159, 10, 0.12); color: #b25000; }

.card-body {
  padding: 2px 14px 12px 54px;
  font-size: 13px; font-weight: 500; line-height: 1.5;
  color: var(--text-1);
  word-break: break-word;
}

.card-foot {
  display: flex; align-items: center; gap: 8px;
  padding: 0 14px 12px 54px;
}

.queue-pill {
  pointer-events: all;
  font-size: 11.5px; font-weight: 600; color: var(--text-3);
  padding: 6px 12px;
  background: var(--surface-raised);
  border: 1px solid var(--border);
  border-radius: 999px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.14);
  cursor: pointer;
}
.queue-pill:hover { color: var(--text-1); }

/* 「跳出」动效：下方向上弹起 + 弹性回弹（弹簧曲线） */
.pop-enter-active { animation: alertPopIn 0.55s cubic-bezier(0.22, 1.4, 0.36, 1) both; }
@keyframes alertPopIn {
  0%   { opacity: 0; transform: translateY(44px) scale(0.8); }
  55%  { opacity: 1; transform: translateY(-10px) scale(1.04); }
  75%  { transform: translateY(4px) scale(0.99); }
  100% { opacity: 1; transform: translateY(0) scale(1); }
}
.pop-leave-active { transition: opacity 0.22s ease, transform 0.22s ease; }
.pop-leave-to { opacity: 0; transform: translateX(28px) scale(0.94); }

@media (prefers-reduced-motion: reduce) {
  .pop-enter-active { animation: none; }
  .pop-leave-active { transition: none; }
}
</style>
