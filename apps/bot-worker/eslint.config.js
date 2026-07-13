// bot-worker ESLint flat config：Node + TypeScript 基线。
// 不复用 @jianmanager/eslint-config（FR-283）：该包是 React/browser 基线，且
// bot-worker 被 pnpm workspace 显式排除（npm 自管，见 pnpm-workspace.yaml），
// 无法走 workspace 协议引用，故独立维护同主版本依赖的 Node 基线。
import { defineConfig, globalIgnores } from 'eslint/config'
import js from '@eslint/js'
import globals from 'globals'
import tseslint from 'typescript-eslint'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.ts'],
    extends: [js.configs.recommended, tseslint.configs.recommended],
    languageOptions: {
      globals: globals.node,
    },
  },
])
