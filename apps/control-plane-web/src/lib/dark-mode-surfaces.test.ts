import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')

function source(path: string): string {
  return readFileSync(resolve(root, path), 'utf8')
}

describe('暗色模式设计表面', () => {
  it('暗色 token 使用 B 方案深色云运维台基调', () => {
    const css = source('index.css')

    expect(css).toContain('--background: #0f141b;')
    expect(css).toContain('--card: #161c24;')
    expect(css).toContain('--primary: #7c86ff;')
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
      'components/console/InstanceConsolePage.tsx',
    ]

    const forbidden = /\b(bg-white(?:\/\d+)?|text-slate-\d+|bg-slate-\d+|border-slate-\d+|bg-amber-50|bg-emerald-50)\b/

    for (const file of files) {
      expect(source(file), file).not.toMatch(forbidden)
    }
  })
})
