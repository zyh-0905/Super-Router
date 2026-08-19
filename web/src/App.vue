<script setup>
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { computed } from 'vue'
import AppSidebar from './components/AppSidebar.vue'
import ToastHost from './components/ToastHost.vue'
import AlertPopupHost from './components/AlertPopupHost.vue'
import Icon from './components/Icon.vue'
import { api } from './api'
import { store, feedAlerts } from './store'

const route = useRoute()
const title = computed(() => route.meta.title || 'Smart Router')

// 更新 document.title
watch(title, (t) => {
  document.title = t === 'Smart Router' ? t : `${t} · Smart Router`
}, { immediate: true })

// 页面切换加载指示
const pageLoading = ref(false)
watch(() => route.path, () => {
  pageLoading.value = true
  // 页面切换动画结束后隐藏
  requestAnimationFrame(() => {
    setTimeout(() => { pageLoading.value = false }, 300)
  })
})

let pingTimer = null
let alertTimer = null

// 告警轮询：30s 拉取 /admin/stats 的告警集合，交由 feedAlerts 做
// 「新出现 / 严重度升级」diff 并弹窗。首次调用仅建立基线、不弹窗；
// 静默失败（未配置 Key 或网络抖动不打扰用户，连接问题由离线横幅提示）。
async function pollAlerts() {
  if (!store.apiKey) return
  try {
    const g = store.currentGroup
    const s = await api.get('/admin/stats' + (g ? `?group_id=${g}` : ''), { silent: true })
    feedAlerts(s && s.alerts)
  } catch { /* silent */ }
}

onMounted(() => {
  api.ping()
  pingTimer = setInterval(() => api.ping(), 20000)
  alertTimer = setInterval(pollAlerts, 30000)
  // 首屏（DashboardView 已建立基线）后再补一次，确保基线完整
  setTimeout(pollAlerts, 5000)
})
onUnmounted(() => {
  clearInterval(pingTimer)
  clearInterval(alertTimer)
})
</script>

<template>
  <!-- 页面切换加载进度条 -->
  <div v-if="pageLoading" class="page-loading-bar" aria-hidden="true" />

  <div class="app-shell">
    <AppSidebar />

    <div class="app-main">
      <!-- 离线横幅 -->
      <Transition name="banner">
        <div v-if="!store.connected" class="offline-bar" role="alert">
          <Icon name="alert" :size="15" aria-hidden="true" />
          <span>无法连接后端服务{{ store.baseURL ? '（' + store.baseURL + '）' : '' }}，请确认 Gateway 已启动。</span>
          <div class="spacer" />
          <router-link to="/settings" class="btn btn-ghost btn-sm">连接设置</router-link>
          <button class="btn btn-primary btn-sm" @click="api.ping()">重试</button>
        </div>
      </Transition>

      <main class="app-content" id="main-content">
        <router-view v-slot="{ Component }">
          <Transition name="page" mode="out-in">
            <component :is="Component" :key="route.path" />
          </Transition>
        </router-view>
      </main>
    </div>
  </div>

  <ToastHost />
  <AlertPopupHost />
</template>

<style scoped>
.app-shell { display: flex; height: 100vh; position: relative; z-index: 1; }
.app-main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.app-content { flex: 1; overflow-y: auto; padding: 30px 40px 56px; }

.offline-bar {
  display: flex; align-items: center; gap: 10px;
  margin: 14px 20px 0;
  padding: 10px 16px;
  background: var(--red-soft);
  border: 1px solid color-mix(in srgb, var(--red) 30%, transparent);
  color: var(--red);
  border-radius: var(--radius-lg);
  font-size: 13px; font-weight: 500;
  flex-shrink: 0;
}
.offline-bar span { color: inherit; }

.page-enter-active, .page-leave-active { transition: opacity 0.18s ease, transform 0.18s ease; }
.page-enter-from { opacity: 0; transform: translateY(6px); }
.page-leave-to { opacity: 0; transform: translateY(-4px); }

.banner-enter-active, .banner-leave-active { transition: all 0.25s ease; }
.banner-enter-from, .banner-leave-to { opacity: 0; transform: translateY(-8px); }

@media (max-width: 860px) {
  .app-content { padding: 18px 16px 40px; }
}
</style>
