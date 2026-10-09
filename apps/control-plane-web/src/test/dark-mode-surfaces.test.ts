import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
/** FR-496：设计 token 已归一到 packages/ui/src/styles/，故暗色 token 断言改读组件包。 */
const repoRoot = resolve(root, '../../..')

function source(path: string): string {
  return readFileSync(resolve(root, path), 'utf8')
}

/** 读取组件包设计底座中的样式文件（token 单一真源）。 */
function uiStyle(file: string): string {
  return readFileSync(resolve(repoRoot, 'packages/ui/src/styles', file), 'utf8')
}

describe('暗色模式设计表面', () => {
  it('暗色 token 使用 B 方案深色云运维台基调', () => {
    const css = uiStyle('tokens.css')

    // 阶段 2 起结构色由种子派生，暗色基调改由 .dark 的**种子**体现：
    // 品牌 #7c86ff（B 方案的紫蓝主操作）与结构色相 257（冷墨蓝）。
    // 派生式里仍能读到暗色的起手明度 0.189（背景）与 0.230（卡片）。
    expect(css).toContain('--seed-brand: #7c86ff;')
    expect(css).toContain('--seed-hue: 257;')
    expect(css).toContain('oklch(0.189')
    expect(css).toContain('oklch(0.230')
    expect(css).toContain('--brand-cobalt: #7c86ff;')
    expect(css).toContain('--workspace-bg-image: none;')
  })

  it('Shell 关键表面有暗色覆盖', () => {
    const css = source('index.css')

    for (const selector of ['.dark .jm-sidebar-drawer', '.dark .jm-console-header', '.dark .jm-toolbar-surface']) {
      expect(css).toContain(selector)
    }
  })

  it('Shell 与统一控制台不再写死浅色专用 Tailwind 类', () => {
    const files = [
      'components/console/ConsoleHeader.tsx',
      'components/console/ConsoleSidebar.tsx',
      // 活着的桌面侧栏（FR-496 阶段 6 起由 DashboardPage 挂载）：旧侧栏仍在名单里当对照物，
      // 但暗色回归必须盯住真正在屏上的那一个。
      'components/views/console/WorkspaceSidebar.tsx',
      'components/console/InstanceConsolePage.tsx',
    ]

    const forbidden = /\b(bg-white(?:\/\d+)?|text-slate-\d+|bg-slate-\d+|border-slate-\d+|bg-amber-50|bg-emerald-50)\b/

    for (const file of files) {
      expect(source(file), file).not.toMatch(forbidden)
    }
  })
})
