import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * 入口必须初始化主语言包。
 *
 * `src/i18n/index.ts` 是**导入即自注册**的（顶层 `i18n.use(initReactI18next).init(...)`），
 * 它不会因为「有人调用了 t()」而被加载——只认模块图。
 *
 * 缺陷成因：入口 `main.tsx` 只引了 `@/lib/logs-federation/i18n`（局部命名空间），主语言包
 * 仅被 `SettingsPage`、`api/plugins`、`ConsoleSidebar` 三处**间接**引入。而这三个都不在入口的
 * 可达闭包里，于是应用启动时主语言包从未注册 —— 全部 `t()` 渲染原始 key
 * （`login.submit`、`nav.workspaceOps`、`storage.clearCacheTitle`），登录页第一屏就中招；
 * 用户走到某个间接引入它的页面才"自己好了"，所以像是偶发。
 *
 * 这类缺陷没有任何运行时报错，单元测试也全绿（测试各文件自己引 i18n），只能从**入口可达性**上守。
 * 故此处做一次静态遍历：从 `main.tsx` 出发，断言 `src/i18n` 在可达闭包内。
 */

const root = path.resolve(__dirname, '..')

/** 把一条 import 说明符解析成 src 下的真实文件；解析不到（包、css、动态拼接）返回 null。 */
function resolveSpecifier(fromFile: string, spec: string): string | null {
  let base: string
  if (spec.startsWith('@/')) base = path.join(root, spec.slice(2))
  else if (spec.startsWith('.')) base = path.resolve(path.dirname(fromFile), spec)
  else return null
  for (const ext of ['.ts', '.tsx', '/index.ts', '/index.tsx']) {
    if (existsSync(base + ext)) return base + ext
  }
  return null
}

/** 从入口文件做可达性遍历（含静态 import；动态 import() 的说明符同样会被正则抓到）。 */
function reachableFrom(entry: string): Set<string> {
  const seen = new Set<string>()
  const stack = [entry]
  while (stack.length > 0) {
    const file = stack.pop()!
    if (seen.has(file) || !existsSync(file)) continue
    seen.add(file)
    const text = readFileSync(file, 'utf8')
    // 【必须同时匹配无 from 的副作用导入】`import '@/i18n'` 没有 `from` 关键字，
    // 只写 `from\s+['"]` 会漏掉它——而本测试要守的恰恰是这种写法（最初就漏了，
    // 导致断言恒真、把「缺 import」也判成通过）。
    for (const m of text.matchAll(/(?:\bfrom\s+|\bimport\s+)['"]([^'"]+)['"]/g)) {
      const resolved = resolveSpecifier(file, m[1])
      if (resolved) stack.push(resolved)
    }
  }
  return seen
}

describe('应用入口的可达性不变量', () => {
  const entry = path.join(root, 'main.tsx')
  const reachable = reachableFrom(entry)

  it('入口可达主语言包（@/i18n）', () => {
    const i18nModule = path.join(root, 'i18n', 'index.ts')
    expect(existsSync(i18nModule), '主语言包模块本身应存在').toBe(true)
    expect(
      reachable.has(i18nModule),
      '入口必须显式 import 主语言包：它导入即自注册，不被间接引用就不会初始化，' +
        '全部 t() 会渲染原始 key（详见本文件顶部说明）',
    ).toBe(true)
  })

  it('入口可达 logs-federation 的局部语言包', () => {
    // 联邦日志的文案在独立命名空间里，同样靠导入即注册，故一并守。
    expect(reachable.has(path.join(root, 'lib', 'logs-federation', 'i18n.ts'))).toBe(true)
  })

  it('可达性遍历本身有效（能走到入口的直接依赖）', () => {
    // 元断言：若解析器坏了、可达集恒为空，上面两条会因为 existsSync 之外的静默失效而误导。
    expect(reachable.has(entry)).toBe(true)
    expect(reachable.has(path.join(root, 'App.tsx'))).toBe(true)
  })
})
