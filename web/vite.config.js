import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发时把 API 请求代理到本地 Gateway（生产环境由 Gateway 直接托管 dist，无需代理）
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/v1': 'http://localhost:8080',
      '/admin': 'http://localhost:8080',
      '/health': 'http://localhost:8080',
      '/metrics': 'http://localhost:8080'
    }
  },
  build: {
    outDir: 'dist',
    rollupOptions: {
      output: {
        manualChunks: {
          echarts: ['echarts'],
          vendor: ['vue', 'vue-router'],
        },
      },
    },
  }
})
