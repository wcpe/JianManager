import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
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

function read(rel: string): string {
  return readFileSync(path.join(srcDir, rel), 'utf8')
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
