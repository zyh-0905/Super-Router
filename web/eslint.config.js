// ============================================================
// ESLint flat config（ESLint 9）
// ============================================================
import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import globals from 'globals'

export default [
  {
    ignores: ['dist/**', 'node_modules/**'],
  },
  // 核心 JS 推荐规则
  js.configs.recommended,
  // Vue 官方 flat/recommended（含 vue-eslint-parser 解析 .vue）
  ...pluginVue.configs['flat/recommended'],
  {
    files: ['**/*.{js,vue}'],
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      globals: {
        ...globals.browser,
      },
    },
    rules: {
      // 组件名不强求多单词（本项目的视图/组件以单文件命名为主）
      'vue/multi-word-component-names': 'off',
      // <script setup> 中供模板使用的变量会被核心规则误报，降级为警告避免阻断
      'no-unused-vars': 'warn',
      // 关闭纯排版风格规则（不影响正确性/安全性），避免大面积告警
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
      'vue/html-self-closing': 'off',
      'vue/html-indent': 'off',
      'vue/first-attribute-linebreak': 'off',
      'vue/html-closing-bracket-newline': 'off',
      'vue/attributes-order': 'off',
      'vue/html-closing-bracket-spacing': 'off',
      'vue/multiline-html-element-content-newline': 'off',
    },
  },
]
