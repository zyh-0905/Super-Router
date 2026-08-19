<script setup>
// 侧边栏：毛玻璃、SF Symbols 图标、连接状态与告警徽标
import { store, cycleTheme, resolvedTheme } from '../store'
import Icon from './Icon.vue'

const nav = [
  { to: '/', name: 'grid', label: '总览' },
  { to: '/channels', name: 'server', label: '站点' },
  { to: '/playground', name: 'beaker', label: '测试台' },
  { to: '/decisions', name: 'list', label: '决策' },
  { to: '/strategy', name: 'gauge', label: '策略中心' },
  { to: '/circuit', name: 'bolt', label: '熔断' },
  { to: '/settings', name: 'gear', label: '设置' },
]

const themeLabel = { auto: '自动', light: '浅色', dark: '深色' }
</script>

<template>
  <aside class="sidebar" role="navigation" aria-label="主导航">
    <!-- Logo -->
    <div class="sb-brand">
      <div class="sb-logo" aria-hidden="true">
        <svg width="22" height="22" viewBox="0 0 64 64" fill="none">
          <rect width="64" height="64" rx="16" fill="#0a84ff" />
          <path d="M20 34l8-14 8 20 8-28 8 22" stroke="white" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </div>
      <div>
        <div class="sb-name">Smart Router</div>
        <div class="sb-sub">网关控制台</div>
      </div>
    </div>

    <!-- 导航 -->
    <nav class="sb-nav">
      <router-link
        v-for="item in nav" :key="item.to"
        :to="item.to" class="sb-item" active-class="active"
      >
        <Icon :name="item.name" :size="17" aria-hidden="true" />
        <span>{{ item.label }}</span>
        <span v-if="item.name === 'bolt' && store.alerts.length" class="sb-badge" :aria-label="store.alerts.length + ' 条告警'">{{ store.alerts.length }}</span>
      </router-link>
    </nav>

    <div class="spacer" />

    <!-- 主题切换 -->
    <div class="sb-bottom">
      <button
        class="sb-item as-btn"
        @click="cycleTheme"
        :title="'主题：' + themeLabel[store.theme] + '（点击切换）'"
        :aria-label="'切换主题，当前：' + themeLabel[store.theme]"
      >
        <Icon :name="resolvedTheme === 'dark' ? 'moon' : store.theme === 'auto' ? 'auto' : 'sun'" :size="17" aria-hidden="true" />
        <span>外观 · {{ themeLabel[store.theme] }}</span>
      </button>

      <!-- 连接状态 -->
      <div class="sb-conn" :class="{ off: !store.connected }" role="status" :aria-label="store.connected ? '已连接到后端' : '后端离线'">
        <span class="dot" :class="store.connected ? 'dot-green dot-pulse' : 'dot-red dot-pulse'" aria-hidden="true" />
        <span class="sb-conn-text">{{ store.connected ? '已连接' : '后端离线' }}</span>
        <span class="sb-conn-epoch mono" v-if="store.epoch != null">Epoch {{ store.epoch }}</span>
      </div>
    </div>
  </aside>
</template>

<style scoped>
.sidebar {
  width: 218px; min-width: 218px; height: 100vh;
  display: flex; flex-direction: column;
  padding: 20px 14px 16px;
  background: var(--bg-gradient-top);
  backdrop-filter: saturate(180%) blur(28px);
  -webkit-backdrop-filter: saturate(180%) blur(28px);
  border-right: 1px solid var(--border);
  z-index: var(--z-sidebar);
}

.sb-brand { display: flex; align-items: center; gap: 10px; padding: 2px 8px 20px; }
.sb-logo { width: 36px; height: 36px; border-radius: 10px; overflow: hidden; box-shadow: 0 4px 12px rgba(10, 132, 255, 0.35); flex-shrink: 0; }
.sb-logo svg { display: block; }
.sb-name { font-size: 14.5px; font-weight: 700; letter-spacing: -0.01em; }
.sb-sub { font-size: 11.5px; color: var(--text-3); margin-top: 1px; }

.sb-nav { display: flex; flex-direction: column; gap: 2px; flex: 1; }

.sb-item {
  display: flex; align-items: center; gap: 11px;
  padding: 9px 12px;
  border-radius: var(--radius-md);
  color: var(--text-2);
  font-size: 13.5px; font-weight: 500;
  text-decoration: none;
  transition: background var(--dur) var(--ease), color var(--dur) var(--ease);
  cursor: pointer; border: none; background: transparent; font-family: inherit;
  width: 100%; text-align: left;
  position: relative;
}
.sb-item:hover { background: var(--surface-hover); color: var(--text-1); }
.sb-item.active { background: var(--blue-soft); color: var(--blue); font-weight: 600; }
.sb-item.as-btn { color: var(--text-2); }
.sb-item.as-btn:hover { background: var(--surface-hover); }

.sb-badge {
  margin-left: auto; min-width: 19px; height: 19px;
  display: inline-flex; align-items: center; justify-content: center;
  padding: 0 6px; border-radius: var(--radius-full);
  background: var(--red); color: #fff;
  font-size: 11px; font-weight: 700;
}

.sb-bottom { display: flex; flex-direction: column; gap: 8px; }

.sb-conn {
  display: flex; align-items: center; gap: 8px;
  margin: 0 4px; padding: 9px 10px;
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  font-size: 12px; color: var(--text-2);
  background: var(--surface);
}
.sb-conn-text { font-weight: 600; }
.sb-conn.off .sb-conn-text { color: var(--red); }
.sb-conn-epoch { margin-left: auto; font-size: 10.5px; color: var(--text-3); }

@media (max-width: 860px) {
  .sidebar { width: 64px; min-width: 64px; padding: 16px 8px; }
  .sb-brand { justify-content: center; padding: 2px 0 16px; }
  .sb-brand > div:last-child { display: none; }
  .sb-item { justify-content: center; padding: 10px; }
  .sb-item span, .sb-conn-text, .sb-conn-epoch { display: none; }
  .sb-badge {
    position: absolute;
    top: 2px; right: 2px;
    min-width: 15px; height: 15px;
    font-size: 9px; padding: 0 4px;
  }
  .sb-conn { justify-content: center; border: none; background: transparent; padding: 8px 0; }
}
</style>
