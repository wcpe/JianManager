import { defineConfig, globalIgnores } from 'eslint/config'
import { reactBase } from '@jianmanager/eslint-config'

export default defineConfig([
  // .tmp/ 是过程性暂存目录（探针脚本、中间产物），本就被 .gitignore 忽略；
  // 它不该让 lint 变红——否则每个临时脚本都会污染仓库级 lint 结果。
  // coverage/ 是覆盖率报告产物。
  globalIgnores(['dist', 'coverage', '.tmp', 'public/mockServiceWorker.js']),
  // 共享 React+TS 基线（FR-283 抽 @jianmanager/eslint-config）作顶层条目
  //（含内部 extends，不能再被 extends 嵌套）；下方同 files 作用域条目为 app 语义叠加。
  reactBase,
  {
    files: ['**/*.{ts,tsx}'],
    rules: {
      // 弃用 shadcn Card 松散用法（FR-163）：阻断业务代码新引入，统一走 Panel / StatCard。
      // @/ 路径为 app 内部语义，故留 app 层不入共享包。
      'no-restricted-imports': [
        'error',
        {
          paths: [
            {
              name: '@/components/ui/card',
              message:
                '弃用 shadcn Card（FR-163）：用 @/components/ui/panel 的 Panel 或 @/components/ui/stat-card 的 StatCard。',
            },
          ],
        },
      ],
    },
  },
])
