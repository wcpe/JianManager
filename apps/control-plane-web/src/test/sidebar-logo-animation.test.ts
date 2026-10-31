/**
 * @file 侧栏折叠时顶栏 logo 不横移的守护测试。
 *
 * 回归来源：用户反馈「收缩会从右边闪现到左边」。实测根因是**折叠态给品牌列加了
 * `justify-content: center`**：
 *
 *   t0 帧：外层仍 246px、按钮已缩到 28px、且被居中 → logo 左缘 = (246-28)/2 = 109px
 *   之后：width 逐帧收窄，居中点跟着左移 → 最终 13px
 *   合计：一趟 96px 的横移，肉眼就是「从右边闪到左边」。
 *
 * 关键判据：`justify-content` 与 `padding-inline` 都**不参与过渡**（规则里只有 width 有
 * transition）。因此折叠态一旦改变这两者的取值，它必然是「一帧瞬跳」而非平滑过渡。
 *
 * jsdom 不做布局，量不出像素，所以这里断言 CSS 源码——这正是会回归的地方：
 * 谁为了「看起来居中」把 center 加回去，这条用例立刻失败。
 */
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const css = readFileSync(resolve(root, 'index.css'), 'utf8')

/** 取出折叠态品牌列那条规则块（选择器含 `data-state='collapsed'` 且指向 top-nav-brand）。 */
function collapsedBrandRule(): string {
  const start = css.indexOf(".jm-console-shell:has([data-slot='console-sidebar'][data-state='collapsed']) .jm-console-header [data-slot='top-nav-brand']")
  expect(start).toBeGreaterThan(-1)
  const end = css.indexOf('}', start)
  expect(end).toBeGreaterThan(start)
  return css.slice(start, end)
}

describe('侧栏折叠：顶栏 logo 不得横移', () => {
  it('折叠态品牌列贴左，不得居中', () => {
    const rule = collapsedBrandRule()
    // center 会让 logo 在外层还没收窄时被推到品牌列右侧（实测 109px）。
    expect(rule).not.toContain('justify-content: center')
    expect(rule).toContain('justify-content: flex-start')
  })

  it('折叠态内边距与展开态同值（padding 不参与过渡，改值即瞬跳）', () => {
    // 具体像素值不重要（顶栏 44px 化时从 16 收到 12），**两态相等**才是不变量：
    // padding 不参与过渡，只要折叠态与展开态取值不同，点击瞬间就是一次瞬跳。
    const collapsed = collapsedBrandRule()
    const expandedStart = css.indexOf(".jm-console-header [data-slot='top-nav-brand']")
    const expanded = css.slice(expandedStart, css.indexOf('}', expandedStart))

    const valueOf = (rule: string) => rule.match(/padding-inline:\s*([0-9]+px)/)?.[1]
    expect(valueOf(collapsed)).toBeDefined()
    expect(valueOf(expanded)).toBeDefined()
    expect(valueOf(collapsed)).toBe(valueOf(expanded))
  })

  it('展开态宽度有过渡（折叠动画的唯一驱动量）', () => {
    const expandedStart = css.indexOf(".jm-console-header [data-slot='top-nav-brand']")
    expect(expandedStart).toBeGreaterThan(-1)
    const expanded = css.slice(expandedStart, css.indexOf('}', expandedStart))
    expect(expanded).toContain('transition: width')
  })
})
