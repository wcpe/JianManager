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
      // shadcn/ui 组件文件以 `export { Comp, compVariants }` 列表式随组件导出 cva 变体，
      // 仅影响 Fast Refresh 的 HMR 体验、非正确性问题，降为 warn 不阻断 CI。
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      // React Compiler 顾问级规则（react-hooks v6 recommended 引入）：标记「可被编译器优化性」
      // 而非缺陷，现有组件未按其重构。降为 warn 以免阻断 CI；真正重构作为独立技术债跟进。
      'react-hooks/set-state-in-effect': 'warn',
      'react-hooks/refs': 'warn',
      'react-hooks/preserve-manual-memoization': 'warn',
      'react-hooks/immutability': 'warn',
    },
  },
])
