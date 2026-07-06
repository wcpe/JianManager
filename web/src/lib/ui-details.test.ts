import { describe, it, expect } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

/**
 * FR-176 全局交互细节修正（增强 FR-163）守护测试。
 *
 * 纯样式约束，故以源文件断言守护三件套（vitest node 环境无 DOM 渲染）：
 * ① 卡片 hover 去位移留阴影（不再 hover:-translate-y，保留 hover:shadow-lift / hover 反馈）；
 * ② 输入焦点环收敛（不再 ring-[3px] + ring-ring/50，焦点态不糊邻近文字）；
 * ③ 全局主题化细滚动条（::-webkit-scrollbar + firefox scrollbar-* 用主题变量，保留 .scrollbar-none）。
 */

const srcDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const rootDir = path.resolve(srcDir, '..')

function existingSourcePath(base: string): string {
  for (const suffix of ['', '.tsx', '.ts']) {
    const candidate = `${base}${suffix}`
    if (existsSync(candidate)) return candidate
  }
  return base
}

function resolveSourcePath(rel: string): string {
  const file = path.join(srcDir, rel)
  const src = readFileSync(file, 'utf8')
  const pureReexport = src.match(/^\s*(?:\/\*[\s\S]*?\*\/\s*)?export \* from ['"]@jianmanager\/ui\/(.+)['"]/)
  if (!pureReexport) return file
  return existingSourcePath(path.join(rootDir, 'packages/ui/src', pureReexport[1]))
}

function read(rel: string): string {
  return readFileSync(resolveSourcePath(rel), 'utf8')
}

function readUiStyles(): string {
  return readFileSync(path.join(rootDir, 'packages/ui/src/styles.css'), 'utf8')
}

/** 既参与 hover 抬升、又需在 FR-176 去位移的卡片/行原语。 */
const HOVER_CARD_FILES = [
  'components/ui/panel.tsx',
  'components/console/NodeWorktableCard.tsx',
  'components/console/BotWorktableCard.tsx',
  'components/console/InstanceWorktableCard.tsx',
  'components/ui/summary-chips.tsx',
  'pages/config-row.tsx',
  'components/console/InventorySegment.tsx',
] as const

describe('FR-176 ① 卡片 hover 去位移留阴影', () => {
  for (const file of HOVER_CARD_FILES) {
    it(`${file} 不含 hover:-translate-y（去位移）`, () => {
      const src = read(file)
      expect(src).not.toMatch(/hover:-translate-y/)
    })
  }

  // shadow-lift 是这些卡片在 FR-163 下的 hover 反馈载体；去位移后阴影必须留存（InventorySegment 单格用 hover:bg-accent 反馈，单列）。
  const SHADOW_LIFT_FILES = HOVER_CARD_FILES.filter((f) => f !== 'components/console/InventorySegment.tsx')
  for (const file of SHADOW_LIFT_FILES) {
    it(`${file} 保留 hover:shadow-lift（阴影反馈不丢）`, () => {
      const src = read(file)
      expect(src).toMatch(/hover:shadow-lift/)
    })
  }

  it('InventorySegment 单格保留 hover:bg-accent（hover 反馈不丢）', () => {
    const src = read('components/console/InventorySegment.tsx')
    expect(src).toMatch(/hover:bg-accent/)
  })
})

describe('FR-176 ② 输入焦点环收敛', () => {
  const src = read('components/ui/input.tsx')

  it('不再使用 3px 焦点环（过粗，糊邻近文字）', () => {
    expect(src).not.toMatch(/focus-visible:ring-\[3px\]/)
  })

  it('不再使用 ring-ring/50 焦点环底色（过浓）', () => {
    expect(src).not.toMatch(/focus-visible:ring-ring\/50/)
  })

  it('保留 focus-visible 焦点环（可见焦点态不丢）', () => {
    expect(src).toMatch(/focus-visible:ring-/)
    expect(src).toMatch(/focus-visible:border-ring/)
  })
})

describe('FR-176 ③ 全局主题化细滚动条', () => {
  const css = read('index.css')

  it('提供 webkit 滚动条样式', () => {
    expect(css).toMatch(/::-webkit-scrollbar/)
    expect(css).toMatch(/::-webkit-scrollbar-thumb/)
  })

  it('提供 firefox 滚动条样式（scrollbar-width + scrollbar-color）', () => {
    // 全局 scrollbar-width 须为 thin（与仅隐藏的 .scrollbar-none:none 区分）。
    expect(css).toMatch(/scrollbar-width:\s*thin/)
    expect(css).toMatch(/scrollbar-color:/)
  })

  it('滚动条配色取主题变量（适配明暗 + 双主题）', () => {
    // 主题化：thumb / track 颜色须引用 CSS 变量而非硬编码十六进制。
    const block = css.slice(css.indexOf('::-webkit-scrollbar'))
    expect(block).toMatch(/var\(--/)
    expect(block).not.toMatch(/#[0-9a-fA-F]{3,6}/)
  })

  it('保留既有 .scrollbar-none（隐藏滚动能力不受影响）', () => {
    expect(css).toMatch(/\.scrollbar-none/)
    expect(css).toMatch(/\.scrollbar-none::-webkit-scrollbar/)
  })
})

describe('FR-244 全局动画 token 化', () => {
  const appCss = read('index.css')
  const uiCss = readUiStyles()

  it('应用与组件包共同暴露 motion duration 与 easing token', () => {
    for (const css of [appCss, uiCss]) {
      expect(css).toMatch(/--motion-duration-fast:\s*120ms/)
      expect(css).toMatch(/--motion-duration-normal:\s*180ms/)
      expect(css).toMatch(/--motion-duration-slow:\s*320ms/)
      expect(css).toMatch(/--motion-easing-standard:\s*cubic-bezier\(0\.16,\s*1,\s*0\.3,\s*1\)/)
      expect(css).toMatch(/--motion-easing-emphasized:\s*ease-out/)
      expect(css).toMatch(/--ease-ios:\s*var\(--motion-easing-standard\)/)
    }
  })

  it('侧栏、顶部进度条、路由过渡使用统一 token', () => {
    expect(appCss).toMatch(/--sidebar-motion-duration:\s*var\(--motion-duration-slow\)/)
    expect(appCss).toMatch(/\.jm-top-loading-track[\s\S]*transition:\s*opacity var\(--motion-duration-fast\) var\(--motion-easing-standard\)/)
    expect(appCss).toMatch(/animation:\s*top-progress var\(--motion-duration-progress-route\) var\(--motion-easing-emphasized\) forwards/)
    expect(appCss).toMatch(/\.jm-route-transition[\s\S]*animation:\s*jm-route-enter var\(--motion-duration-route\) var\(--motion-easing-standard\)/)
  })

  it('抽屉和工具条交互不再直接写散落毫秒值', () => {
    expect(appCss).toMatch(/\.jm-sidebar-group-content[\s\S]*grid-template-rows var\(--motion-duration-normal\)/)
    expect(appCss).toMatch(/\.jm-mobile-nav-panel\[data-state='open'\][\s\S]*animation:\s*jm-panel-up var\(--motion-duration-normal\) var\(--motion-easing-standard\)/)
    expect(appCss).toMatch(/\.jm-toolbar-surface[\s\S]*transition:\s*background-color var\(--motion-duration-normal\) var\(--motion-easing-standard\)/)
  })
})
