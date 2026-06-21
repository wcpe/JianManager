import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      // react-refresh 保持 error 以拦截新组件文件的非组件导出；
      // shadcn(badge/button/tabs) 与 RangePicker 随组件导出变体/常量，已在各文件顶部按需豁免。
      // React Compiler 顾问规则（set-state-in-effect/refs/immutability/preserve-manual-memoization）
      // 沿用 react-hooks recommended 的 error 级；既有合法模式已逐处 eslint-disable 并注明理由。
      'react-refresh/only-export-components': 'error',
    },
  },
])
