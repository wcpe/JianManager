import { useMemo } from 'react'
import { meetsContrast, parseColor, wcagContrast } from '@jianmanager/ui/lib/color-contrast'

/**
 * 主题矩阵验证台（FR-496 阶段 7）。
 *
 * ── 为什么需要它 ──
 * 阶段 2 把主题改成种子声明式后，「新增一套主题只写 5 个种子」这条承诺能否兑现，
 * 取决于派生出的结构色是否真的可用。对比度有测试守着（color-contrast.test.ts），
 * 但那守的是**阈值**，看不出「色相同族吗」「明暗关系对吗」——那些得用眼睛看。
 *
 * ── 实现要点：为什么不切全局主题 ──
 * 主题靠 `[data-theme="x"]` / `[data-theme="x"].dark` 两个选择器生效，而它们只看祖先链，
 * 因此**每个格子可以各自挂一对属性形成独立作用域**——不需要来回切 html 根上的属性，
 * 也就不会与页面其它部分的当前主题互相干扰。
 *
 * 对比度复用 `lib/color-contrast`（与测试同一份实现），于是「这里显示的数字」与
 * 「测试断言的值」不可能漂移——两边算的是同一套公式。
 */

/** 与 color-contrast.test.ts 的 CONTRAST_PAIRS 同口径，只取最易失守的几对。 */
const PAIRS: { fg: string; bg: string; min: number; usage: string }[] = [
  { fg: '--foreground', bg: '--background', min: 4.5, usage: '正文' },
  { fg: '--card-foreground', bg: '--card', min: 4.5, usage: '卡片正文' },
  { fg: '--primary-foreground', bg: '--primary', min: 4.5, usage: '按钮文字' },
  { fg: '--accent-foreground', bg: '--accent', min: 4.5, usage: '淡色块文字' },
  { fg: '--muted-foreground', bg: '--background', min: 3, usage: '次要说明' },
]

const THEMES = ['indigo', 'teal', 'ocean', 'violet', 'sunset'] as const
const MODES = ['light', 'dark'] as const

/** 一个主题作用域下读取若干 token 的实际计算值。 */
function useResolvedTokens(theme: string, mode: 'light' | 'dark', names: string[]): Record<string, string> {
  // 计算值只能从真实 DOM 取：oklch/calc 的求值结果在 computed style 里，
  // 而 CSSStyleDeclaration 不暴露「任意元素的 var 展开结果」，故借助一个隐藏探针元素。
  return useMemo(() => {
    if (typeof document === 'undefined') return {}
    const probe = document.createElement('div')
    probe.setAttribute('data-theme', theme)
    if (mode === 'dark') probe.classList.add('dark')
    probe.style.cssText = 'position:absolute;visibility:hidden;pointer-events:none'
    document.body.appendChild(probe)
    const cs = getComputedStyle(probe)
    const result = Object.fromEntries(names.map((n) => [n, cs.getPropertyValue(n).trim()]))
    probe.remove()
    return result
    // theme/mode 变化时重算；names 是模块级常量
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [theme, mode])
}

const ALL_TOKENS = [
  '--background',
  '--foreground',
  '--card',
  '--primary',
  '--primary-foreground',
  '--accent',
  '--accent-foreground',
  '--border',
  '--muted-foreground',
]

/** 一个主题 × 明暗的样张卡片。 */
function ThemeCell({ theme, mode }: { theme: string; mode: 'light' | 'dark' }) {
  const tokens = useResolvedTokens(theme, mode, ALL_TOKENS)

  const contrast = PAIRS.map((pair) => {
    const fg = tokens[pair.fg]
    const bg = tokens[pair.bg]
    if (!fg || !bg || parseColor(fg) === null || parseColor(bg) === null) {
      return { ...pair, ratio: null as number | null, ok: false }
    }
    // 探针读到的可能是未化简的 calc；parseColor 已支持，故直接算。
    const ratio = wcagContrast(fg, bg)
    return { ...pair, ratio, ok: meetsContrast(fg, bg, pair.min) }
  })

  const scopeProps = {
    ...(theme === 'indigo' ? {} : { 'data-theme': theme }),
    ...(mode === 'dark' ? { className: 'dark' } : {}),
  }

  return (
    <div {...scopeProps} className={`${scopeProps.className ?? ''} rounded-lg border border-border bg-background p-3`}>
      <div className="mb-2 flex items-baseline justify-between">
        <span className="text-sm font-medium text-foreground">
          {theme} · {mode === 'dark' ? '暗' : '亮'}
        </span>
        <span className="text-[11px] text-muted-foreground">{theme === 'indigo' ? '默认' : 'data-theme'}</span>
      </div>

      {/* 色块：直接铺 token，肉眼即可判断色相同族与明暗关系 */}
      <div className="mb-2 grid grid-cols-3 gap-1">
        {['--background', '--card', '--border', '--primary', '--accent', '--muted-foreground'].map((name) => (
          <div key={name} className="rounded border border-border/60 px-1.5 py-1 text-[10px] text-muted-foreground">
            <div className="mb-1 h-5 rounded-sm" style={{ background: `var(${name})` }} />
            {name.replace(/^--/, '')}
          </div>
        ))}
      </div>

      {/* 真实排版样张：色块看不出可读性，文字才能 */}
      <div className="mb-2 rounded border border-border bg-card p-2">
        <p className="text-sm text-card-foreground">卡片正文 Card foreground</p>
        <p className="text-[11px] text-muted-foreground">次要说明 Muted foreground</p>
        <div className="mt-1 flex gap-1">
          <span className="rounded bg-primary px-2 py-0.5 text-[11px] text-primary-foreground">主操作</span>
          <span className="rounded bg-accent px-2 py-0.5 text-[11px] text-accent-foreground">淡色块</span>
        </div>
      </div>

      {/* 对比度：与测试同口径，任一不达标即标红 */}
      <ul className="space-y-0.5">
        {contrast.map((pair) => (
          <li key={pair.usage} className="flex items-baseline justify-between text-[10px]">
            <span className="text-muted-foreground">{pair.usage}</span>
            <span className={pair.ok ? 'text-status-success' : 'text-status-danger'}>
              {pair.ratio === null ? '解析失败' : `${pair.ratio.toFixed(2)}:1`}
              {pair.ok ? '' : ` < ${pair.min}`}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

/** 主题矩阵：5 主题 × 2 明暗，每格是独立作用域的样张。 */
export function ThemeMatrix() {
  return (
    <section className="space-y-3">
      <header>
        <h2 className="text-sm font-medium text-foreground">主题矩阵 · 5 主题 × 明暗</h2>
        <p className="text-[11px] text-muted-foreground">
          每格是独立作用域（挂 <code>data-theme</code> / <code>dark</code> 属性），互不干扰。
          对比度复用 <code>lib/color-contrast</code>，与
          <code>color-contrast.test.ts</code> 同一份实现——这里红的就是测试会挂的。
        </p>
      </header>
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {MODES.flatMap((mode) => THEMES.map((theme) => <ThemeCell key={`${theme}-${mode}`} theme={theme} mode={mode} />))}
      </div>
    </section>
  )
}
