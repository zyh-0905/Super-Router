<script setup>
import { onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { computed } from 'vue'
import AppSidebar from './components/AppSidebar.vue'
import ToastHost from './components/ToastHost.vue'
import Icon from './components/Icon.vue'
import { api } from './api'
import { store } from './store'

const route = useRoute()
const title = computed(() => route.meta.title || 'Smart Router')

let pingTimer = null

onMounted(() => {
  api.ping()
  pingTimer = setInterval(() => api.ping(), 20000)
})
onUnmounted(() => clearInterval(pingTimer))
</script>

<template>
  <div class="app-shell">
    <AppSidebar />

    <div class="app-main">
      <!-- 离线横幅 -->
      <Transition name="banner">
        <div v-if="!store.connected" class="offline-bar">
          <Icon name="alert" :size="15" />
          <span>无法连接后端服务{{ store.baseURL ? '（' + store.baseURL + '）' : '' }}，请确认 Gateway 已启动。</span>
          <div class="spacer" />
          <router-link to="/settings" class="btn btn-ghost btn-sm">连接设置</router-link>
          <button class="btn btn-primary btn-sm" @click="api.ping()">重试</button>
        </div>
      </Transition>

      <main class="app-content">
        <router-view v-slot="{ Component }">
          <Transition name="page" mode="out-in">
            <component :is="Component" :key="route.path" />
          </Transition>
        </router-view>
      </main>
    </div>
  </div>

  <ToastHost />
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
