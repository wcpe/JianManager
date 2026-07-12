// 共享 ESLint flat config（FR-283，见 ADR-064）：React + TypeScript 基线。
// 只承载与仓库无关的通用基线；app 语义规则（如 @/ 路径的 no-restricted-imports
// 弃用清单）留在各 app 的 eslint.config.js 叠加，避免共享层耦合 app 内部路径。
import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'

/** React + TS 基线（files 作用域 **\/*.{ts,tsx}），供 defineConfig extends 复用。 */
export const reactBase = {
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
    // 组件文件随组件导出变体/常量的合法场景在各文件顶部按需豁免。
    'react-refresh/only-export-components': 'error',
  },
}

export default reactBase
