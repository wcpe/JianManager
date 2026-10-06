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
// FR-283：packages/ui 迁仓库根 packages/，rootDir 指仓库根。
const rootDir = path.resolve(srcDir, '../../..')

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

/**
 * 读应用侧源码。`@ui/` 前缀表示「本体已迁入组件库」（ADR-097），此时读包内实现——
 * 应用侧那个文件只是接线层（取数 + 注入），断言要落的是实现。
 *
 * 不用「文件里出现 views 导入就跳转」这类启发式：普通组件也会导入 views 组件
 * （例如 BotWorktableCard 引 BotHealthBar），那样会被误判成接线层、读到别人的源码。
 */
function read(rel: string): string {
  if (rel.startsWith('@ui/')) {
    return readFileSync(existingSourcePath(path.join(rootDir, 'packages/ui/src', rel.slice(4))), 'utf8')
  }
  return readFileSync(resolveSourcePath(rel), 'utf8')
}

/**
 * FR-496：组件包的设计底座已从单一 styles.css 拆分为 styles/ 目录下的多文件
 * （theme-map / tokens / themes / motion），此处合并读取以保持既有断言成立。
 */
function readUiStyles(): string {
  const dir = path.join(rootDir, 'packages/ui/src/styles')
  return ['theme-map.css', 'tokens.css', 'themes.css', 'motion.css']
    .map((f) => readFileSync(path.join(dir, f), 'utf8'))
    .join('\n')
}
/** 既参与 hover 抬升、又需在 FR-176 去位移的卡片/行原语。 */
const HOVER_CARD_FILES = [
  'components/ui/panel.tsx',
  'components/console/NodeWorktableCard.tsx',
  'components/console/BotWorktableCard.tsx',
  '@ui/components/views/instances/InstanceWorktableCard.tsx',
  'components/ui/summary-chips.tsx',
  'pages/config-row.tsx',
  '@ui/components/views/instances/InventorySegment.tsx',
] as const

describe('FR-176 ① 卡片 hover 去位移留阴影', () => {
  for (const file of HOVER_CARD_FILES) {
    it(`${file} 不含 hover:-translate-y（去位移）`, () => {
      const src = read(file)
      expect(src).not.toMatch(/hover:-translate-y/)
    })
  }

  // shadow-lift 是这些卡片在 FR-163 下的 hover 反馈载体；去位移后阴影必须留存（InventorySegment 单格用 hover:bg-accent 反馈，单列）。
  const SHADOW_LIFT_FILES = HOVER_CARD_FILES.filter((f) => f !== '@ui/components/views/instances/InventorySegment.tsx')
  for (const file of SHADOW_LIFT_FILES) {
    it(`${file} 保留 hover:shadow-lift（阴影反馈不丢）`, () => {
      const src = read(file)
      expect(src).toMatch(/hover:shadow-lift/)
    })
  }

  it('InventorySegment 单格保留 hover:bg-accent（hover 反馈不丢）', () => {
    const src = read('@ui/components/views/instances/InventorySegment.tsx')
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

  it('组件包设计底座暴露 motion duration 与 easing token（应用经 @import 引入）', () => {
    // FR-496：token 已归一到 packages/ui/src/styles/，应用不再重复定义
    // （此前 app 与包各持一份副本、已发生漂移，本次合并为单一真源）。
    for (const css of [uiCss]) {
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

  it('Dialog 内 Combobox/Select 浮层抬高到 300 且遮罩不吃点击', () => {
    expect(appCss).toMatch(
      /body:has\(\[data-slot="combobox-content"\]\[data-state="open"\]\) \[data-slot="dialog-overlay"\][\s\S]*pointer-events:\s*none !important/,
    )
    expect(appCss).toMatch(
      /body:has\(\[data-slot="combobox-content"\]\[data-state="open"\]\) \[data-radix-popper-content-wrapper\][\s\S]*z-index:\s*300 !important/,
    )
  })

  // 全站动效收敛（FR-244 剩余缺口）：共享原语的一次性交互动效从散落的 duration-200/300 收敛到 motion token。
  // 每项断言「不含硬编码定值」+「引用 motion-duration token」，防新代码或回退再引入脱离节奏的毫秒值。
  const CONVERGED_MOTION_PRIMITIVES = [
    'components/ui/panel.tsx',
    'components/ui/dialog.tsx',
    'components/ui/summary-chips.tsx',
    'components/ui/view-toggle.tsx',
  ] as const

  for (const file of CONVERGED_MOTION_PRIMITIVES) {
    it(`${file} hover/浮层动效绑定 motion-duration token（不再硬编码 duration-200/300）`, () => {
      const src = read(file)
      expect(src).not.toMatch(/duration-200\b/)
      expect(src).not.toMatch(/duration-300\b/)
      // FR-496：动效取值可以写在调用点（字面量），也可以抽到 packages/ui/src/lib 的共享常量。
      // 后者更干净（取值单一真源），故两种写法都接受；但必须能追溯到 motion token ——
      // 字面量在本文件可验，常量引用则由下一条断言在常量文件里验。
      const literal = /duration-\[var\(--motion-duration-(?:normal|slow)\)\]/.test(src)
      const viaConstant = /\b(?:shadowTransition|interactionTransition)\b/.test(src)
      expect(literal || viaConstant, '既无 motion token 字面量，也未引用共享动效常量').toBe(true)
    })
  }

  it('共享动效常量本身绑定 motion-duration token（FR-496 收敛）', () => {
    const lib = readFileSync(path.join(rootDir, 'packages/ui/src/lib/interaction-overlay.ts'), 'utf8')
    expect(lib).toMatch(/duration-\[var\(--motion-duration-(?:fast|normal|slow)\)\]/)
    expect(lib).not.toMatch(/duration-200\b/)
    expect(lib).not.toMatch(/duration-300\b/)
  })

  it('组件包设计底座也暴露收敛注释指向统一动画词汇表', () => {
    expect(uiCss).toMatch(/FR-244/)
    expect(uiCss).toMatch(/--motion-duration-slow/)
  })
})
