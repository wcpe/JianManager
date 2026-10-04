import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

/**
 * 控制台外壳折叠动画契约（FR-496 阶段 6 补丁）。
 *
 * 抖动根因是「两个时钟 + 内容区位移补偿」：外壳用 `sidebarLayout`/`sidebarMotion` 状态机
 * 在 320ms 后落位，同时靠 `.jm-console-content` 的 `clip-path` + `translate3d` 把侧栏宽度变化
 * 「补」回来。JS 定时器与 CSS 过渡只要差一帧，落位瞬间内容区就会跳一下。
 * 修法是把宽度过渡交回侧栏自身、并删掉内容区补偿——**内容区只是同一 flex 行的兄弟节点**。
 *
 * jsdom 不做样式计算，故按仓库既有做法（`lib/ui-details.test.ts`）直接断言源 CSS：
 * 这些约束一旦被改回去，用例立刻失败，避免抖动复发。
 */
const cssPath = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../index.css')
const css = readFileSync(cssPath, 'utf8')

/** 取某个选择器的声明块（首个匹配），用于断言「该选择器写了什么」而不是「文件里出现过什么」。 */
function block(selector: string): string {
  const start = css.indexOf(`${selector} {`)
  expect(start, `选择器未找到：${selector}`).toBeGreaterThan(-1)
  return css.slice(start, css.indexOf('}', start))
}

describe('控制台外壳折叠动画（FR-496 阶段 6 补丁）', () => {
  it('宽度过渡只作用于侧栏自身，且复用 motion token', () => {
    const sidebar = block('.jm-console-sidebar')
    expect(sidebar).toMatch(/transition:\s*width var\(--sidebar-motion-duration\) var\(--motion-easing-standard\)/)
    // 折叠基准宽度仍取外壳变量（246px / 54px，照原型 `.sidebar`）。
    expect(sidebar).toMatch(/width:\s*var\(--sidebar-expanded-width\)/)
    expect(block(".jm-console-sidebar[data-state='collapsed']")).toMatch(/width:\s*var\(--sidebar-collapsed-width\)/)
  })

  it('内层抽屉跟随外层宽度，不再自己过渡（两个元素各持一份过渡是抖动来源之一）', () => {
    const drawer = block('.jm-sidebar-drawer')
    expect(drawer).toMatch(/width:\s*100%/)
    expect(drawer).toMatch(/transition:\s*none/)
    expect(drawer).not.toMatch(/transition:\s*width/)
  })

  it('内容区不做位移 / 裁剪补偿，也不参与过渡', () => {
    const content = block('.jm-console-content')
    expect(content).toMatch(/min-width:\s*0/)
    expect(content).not.toMatch(/translate/)
    expect(content).not.toMatch(/clip-path/)
    expect(content).not.toMatch(/transition/)
    // 旧状态机的两个钩子整段退场：外壳不再靠 `data-sidebar-motion` 驱动任何补偿动画。
    expect(css).not.toMatch(/data-sidebar-motion/)
  })
})
