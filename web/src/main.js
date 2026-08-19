import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import App from './App.vue'
import { store, applyTheme } from './store'

import './styles/tokens.css'
import './styles/base.css'

// ===== 路由（hash 模式：兼容静态托管与深链接） =====
import DashboardView from './views/DashboardView.vue'
import ChannelsView from './views/ChannelsView.vue'
import PlaygroundView from './views/PlaygroundView.vue'
import DecisionsView from './views/DecisionsView.vue'
import CircuitView from './views/CircuitView.vue'
import SettingsView from './views/SettingsView.vue'
import StrategyView from './views/StrategyView.vue'
import AlertsView from './views/AlertsView.vue'

const routes = [
  { path: '/', name: 'dashboard', component: DashboardView, meta: { title: '总览' } },
  { path: '/channels', name: 'channels', component: ChannelsView, meta: { title: '站点' } },
  { path: '/playground', name: 'playground', component: PlaygroundView, meta: { title: '测试台' } },
  { path: '/decisions', name: 'decisions', component: DecisionsView, meta: { title: '决策' } },
  { path: '/strategy', name: 'strategy', component: StrategyView, meta: { title: '策略中心' } },
  { path: '/circuit', name: 'circuit', component: CircuitView, meta: { title: '熔断' } },
  { path: '/alerts', name: 'alerts', component: AlertsView, meta: { title: '告警' } },
  { path: '/settings', name: 'settings', component: SettingsView, meta: { title: '设置' } },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

// ===== 应用启动 =====
applyTheme()

// 跟随系统变化（用户未手动选择时）
window.matchMedia?.('(prefers-color-scheme: dark)')?.addEventListener('change', () => {
  if (store.theme === 'auto') applyTheme()
})

createApp(App).use(router).mount('#app')
