/*
 * 用 node:fs 直接读源文件。`@ts-expect-error` 是必需的：packages/ui 的 tsconfig `types` 只声明
 * vite/client（不含 node），而本文件由 vitest 的 node project 执行、确实能加载 node 内置模块。
 * 不为此给组件包补 node 类型依赖，也不用 Vite 的 `?raw`——后者会被 vitest 的 CSS 兜底
 * （css: false 时把 .css 模块置空）清成空字符串，读不到真实文本，也就守不住源头。
 */
// @ts-expect-error 见上方说明：node 内置模块类型不在本包 tsconfig 的 types 列表内
import { readFileSync } from 'node:fs'

import { describe, expect, it } from 'vitest'

import { meetsContrast, parseColor, parseOklch, toHex, wcagContrast } from './color-contrast'

/**
 * 5 套主题 × 明暗 = 10 组配色的 WCAG 对比度自动校验（FR-496 阶段 2）。
 *
 * ── 为什么 ──
 * themes.css 里至今保留着人工验算痕迹（「近白对 oklch(0.704) 青绿仅 2.39:1，不达 4.5:1；
 * 深青墨实测 7.18:1」）。人工验算不可持续，也没有任何闸门阻止改主题色时对比度劣化。
 * 本文件把「人工算一次」变成「每次改动自动算」：CI 一旦发现某对前景/背景低于阈值即变红。
 *
 * ── 关键设计：值必须来自 CSS 文件，不能抄进测试 ──
 * 下面用一个极简 CSS 解析器**真实读取** tokens.css / themes.css，按 CSS 层叠规则合并出
 * 每套主题 × 明暗的最终变量表，再拿合并结果算对比度。若把颜色值在测试里再抄一份，
 * 测试守住的就只是「抄件」，源头改了照样绿——那就等于没有闸门。
 */

// ───────────────────────── 颜色取值：从 CSS 真实读取 ─────────────────────────

/** styles/ 目录（本文件位于 src/lib/）。 */
const STYLE_DIR = new URL('../styles/', import.meta.url)

function readStyleSource(fileName: 'tokens.css' | 'themes.css'): string {
  return readFileSync(new URL(fileName, STYLE_DIR), 'utf8')
}

/** 一个作用域内的声明（只保留自定义属性）。 */
type DeclarationMap = Map<string, string>

/**
 * 一张样式表按选择器归类后的结果。
 * light/dark 是 `:root` / `.dark` 基线；themes 是带 data-theme 的主题块（键为主题名）。
 */
interface Stylesheet {
  light: DeclarationMap
  dark: DeclarationMap
  themeLight: Map<string, DeclarationMap>
  themeDark: Map<string, DeclarationMap>
  /** 未被识别、因此不会参与合并的选择器——静默丢弃会让校验基于错误的值，故必须为空。 */
  unknownSelectors: string[]
}

/** 选择器 → 作用域类别。 */
function classifySelector(selector: string): { kind: 'light' | 'dark' | 'themeLight' | 'themeDark'; theme?: string } | null {
  const text = selector.replace(/\s+/g, '')

  if (text === ':root') return { kind: 'light' }
  if (text === '.dark') return { kind: 'dark' }

  const themeLight = /^\[data-theme=["']?([\w-]+)["']?\]$/.exec(text)
  if (themeLight) return { kind: 'themeLight', theme: themeLight[1] }

  const themeDark = /^\[data-theme=["']?([\w-]+)["']?\]\.dark$/.exec(text)
  if (themeDark) return { kind: 'themeDark', theme: themeDark[1] }

  return null
}

/** 块级 at-rule（@media/@layer/@supports）会改变层叠语义，简易解析器不展开——宁可报错也不静默算错。 */
const BLOCK_AT_RULE_PATTERN = /@[a-z-]+[^{;]*\{/i

/**
 * 极简 CSS 解析：只处理 `选择器 { --var: value; }` 这一形态。
 * 支持范围刻意收窄到本仓库 token 文件的实际写法；遇到超出范围的写法会抛错而不是猜。
 */
function parseStylesheet(css: string, sourceName: string): Stylesheet {
  // 注释里含冒号、引号甚至伪代码，必须先剔除，否则会干扰块与声明的切分
  const withoutComments = css.replace(/\/\*[\s\S]*?\*\//g, '')

  if (BLOCK_AT_RULE_PATTERN.test(withoutComments)) {
    throw new Error(
      `${sourceName} 出现了块级 at-rule（@media/@supports/@layer）：简易解析器无法还原其层叠语义，` +
        `请先扩展 parseStylesheet 再依赖本文件的对比度断言`,
    )
  }

  const sheet: Stylesheet = {
    light: new Map(),
    dark: new Map(),
    themeLight: new Map(),
    themeDark: new Map(),
    unknownSelectors: [],
  }

  const blockPattern = /([^{}]+)\{([^{}]*)\}/g
  for (const block of withoutComments.matchAll(blockPattern)) {
    const selectors = block[1]
      .split(',')
      .map((selector) => selector.trim())
      .filter(Boolean)
    const declarations = parseDeclarations(block[2], sourceName)

    for (const selector of selectors) {
      const classified = classifySelector(selector)
      if (!classified) {
        // 例：将来有人把主题块写成 `html[data-theme="x"]`，这里会立刻暴露而不是被悄悄跳过
        sheet.unknownSelectors.push(selector)
        continue
      }

      switch (classified.kind) {
        case 'light':
          for (const [name, value] of declarations) sheet.light.set(name, value)
          break
        case 'dark':
          for (const [name, value] of declarations) sheet.dark.set(name, value)
          break
        case 'themeLight':
        case 'themeDark': {
          const bucket = classified.kind === 'themeLight' ? sheet.themeLight : sheet.themeDark
          const theme = classified.theme as string
          const target = bucket.get(theme) ?? new Map<string, string>()
          for (const [name, value] of declarations) target.set(name, value)
          bucket.set(theme, target)
          break
        }
      }
    }
  }

  return sheet
}

/** 解析块内的 `--var: value;` 声明。 */
function parseDeclarations(body: string, sourceName: string): DeclarationMap {
  const declarations: DeclarationMap = new Map()

  for (const raw of body.split(';')) {
    const text = raw.trim()
    if (!text) continue

    const colon = text.indexOf(':')
    if (colon === -1) continue

    const name = text.slice(0, colon).trim()
    if (!name.startsWith('--')) continue

    const value = text.slice(colon + 1).trim()
    if (/!important$/i.test(value)) {
      // 合并逻辑按「书写顺序即优先级」还原层叠，!important 会推翻这个前提
      throw new Error(`${sourceName} 的 ${name} 使用了 !important：本文件按书写顺序还原层叠，无法表达 !important`)
    }

    declarations.set(name, value)
  }

  return declarations
}

/** 按顺序合并多层变量，后者覆盖前者（= CSS 同名属性后者胜出）。 */
function mergeLayers(layers: Array<DeclarationMap | undefined>): DeclarationMap {
  const merged: DeclarationMap = new Map()
  for (const layer of layers) {
    if (!layer) continue
    for (const [name, value] of layer) merged.set(name, value)
  }
  return merged
}

/**
 * 还原某套主题在某明暗下的最终变量表。
 *
 * 层级顺序严格按 index.css 的 import 顺序（theme-map → tokens → themes）与选择器特异性推导：
 *   亮色：tokens `:root` → themes `:root` → `[data-theme=x]`
 *   暗色：tokens `:root` → themes `:root` → tokens `.dark` → themes `.dark`
 *         → `[data-theme=x]`（特异性同单类，书写在后而胜出）→ `[data-theme=x].dark`（特异性更高）
 * indigo 是默认主题，没有 `[data-theme]` 块，于是亮色=`:root`、暗色=`:root`+`.dark`。
 */
function cascadeVars(
  tokens: Stylesheet,
  themes: Stylesheet,
  theme: string,
  mode: 'light' | 'dark',
): DeclarationMap {
  const lightThemeBlock = themes.themeLight.get(theme)
  if (mode === 'light') {
    return mergeLayers([tokens.light, themes.light, lightThemeBlock])
  }
  return mergeLayers([
    tokens.light,
    themes.light,
    tokens.dark,
    themes.dark,
    lightThemeBlock,
    themes.themeDark.get(theme),
  ])
}

const TOKENS = parseStylesheet(readStyleSource('tokens.css'), 'tokens.css')
const THEMES = parseStylesheet(readStyleSource('themes.css'), 'themes.css')

/** 默认主题名：不写 `[data-theme]` 即为它。 */
const DEFAULT_THEME = 'indigo'

/** 主题清单：默认主题 + themes.css 里定义了块的主题（新增主题会自动纳入校验）。 */
const THEME_NAMES = [DEFAULT_THEME, ...THEMES.themeLight.keys()].filter(
  (theme, index) => theme !== DEFAULT_THEME || index === 0,
)

const MODES = ['light', 'dark'] as const
type Mode = (typeof MODES)[number]

interface ThemeCell {
  theme: string
  mode: Mode
  /** 用例标题与失败信息里的定位标签，形如 `teal·dark`。 */
  label: string
  vars: DeclarationMap
}

/** 10 组配色（5 主题 × 2 明暗）的最终变量表。 */
const MATRIX: ThemeCell[] = THEME_NAMES.flatMap((theme) =>
  MODES.map((mode) => ({ theme, mode, label: `${theme}·${mode}`, vars: cascadeVars(TOKENS, THEMES, theme, mode) })),
)

// ───────────────────────── 待校验的配对与阈值 ─────────────────────────

interface ContrastPair {
  fg: string
  bg: string
  minRatio: number
  usage: string
}

/** 阈值取自 WCAG 2.x：正文 4.5:1（AA 常规文本），状态徽章这类大字/图形降到 3:1（AA 非文本）。 */
const CONTRAST_PAIRS: ContrastPair[] = [
  { fg: '--foreground', bg: '--background', minRatio: 4.5, usage: '正文' },
  { fg: '--card-foreground', bg: '--card', minRatio: 4.5, usage: '卡片正文' },
  { fg: '--popover-foreground', bg: '--popover', minRatio: 4.5, usage: '浮层正文' },
  { fg: '--primary-foreground', bg: '--primary', minRatio: 4.5, usage: '按钮文字（最易失守的一对）' },
  { fg: '--secondary-foreground', bg: '--secondary', minRatio: 4.5, usage: '次要按钮' },
  { fg: '--accent-foreground', bg: '--accent', minRatio: 4.5, usage: '淡色块文字' },
  { fg: '--muted-foreground', bg: '--background', minRatio: 3, usage: '次要说明文字' },
  { fg: '--status-success-foreground', bg: '--status-success', minRatio: 3, usage: '成功徽章' },
  { fg: '--status-danger-foreground', bg: '--status-danger', minRatio: 3, usage: '危险徽章' },
  { fg: '--status-warning-foreground', bg: '--status-warning', minRatio: 3, usage: '警告徽章' },
  { fg: '--status-info-foreground', bg: '--status-info', minRatio: 3, usage: '信息徽章' },
]

/** 取变量值，缺失即报错——缺定义会让对比度校验悄悄跳过，必须显式失败。 */
function requireVar(vars: DeclarationMap, name: string, where: string): string {
  const value = vars.get(name)
  if (!value) throw new Error(`${where} 未定义 ${name}（合并后的变量表里缺失）`)
  return value
}

// ───────────────────────── 颜色工具自身的用例 ─────────────────────────

describe('颜色解析与转换', () => {
  it('解析 hex：#rgb 与 #rrggbb、大小写皆可', () => {
    expect(parseColor('#fff')).toEqual({ r: 255, g: 255, b: 255 })
    expect(parseColor('#158053')).toEqual({ r: 21, g: 128, b: 83 })
    expect(parseColor('#FFFFFF')).toEqual({ r: 255, g: 255, b: 255 })
    expect(parseColor('#12345')).toBeNull()
  })

  it('解析 oklch：L 支持小数与百分号，H 支持角度单位', () => {
    expect(parseOklch('oklch(0.704 0.123 183)')).toEqual({ l: 0.704, c: 0.123, h: 183 })
    const percent = parseOklch('oklch(70.4% 0.123 183)')
    expect(percent?.l).toBeCloseTo(0.704, 10)
    expect(percent?.c).toBeCloseTo(0.123, 10)
    expect(parseOklch('oklch(0.704 0.123 183deg)')?.h).toBeCloseTo(183, 10)
    // 百分号与小数写法必须解析出完全相同的 sRGB
    expect(parseColor('oklch(70.4% 0.123 183)')).toEqual(parseColor('oklch(0.704 0.123 183)'))
  })

  it('解析 rgb()：空格/逗号分隔与百分比分量', () => {
    expect(parseColor('rgb(20 184 166)')).toEqual({ r: 20, g: 184, b: 166 })
    expect(parseColor('rgb(20, 184, 166)')).toEqual({ r: 20, g: 184, b: 166 })
    expect(parseColor('rgb(100% 100% 100%)')).toEqual({ r: 255, g: 255, b: 255 })
    // --brand-shadow / --shadow-color 的裸分量写法
    expect(parseColor('20 184 166')).toEqual({ r: 20, g: 184, b: 166 })
  })

  it('带 alpha 的写法解析出不透明基色，alpha 被忽略（模块注释已声明该边界）', () => {
    expect(parseColor('rgba(255, 0, 0, 0.5)')).toEqual({ r: 255, g: 0, b: 0 })
    expect(parseColor('oklch(0.704 0.123 183 / 40%)')).toEqual(parseColor('oklch(0.704 0.123 183)'))
  })

  it('无法识别的写法返回 null，而不是硬猜', () => {
    expect(parseColor('color-mix(in srgb, var(--primary) 13%, var(--card))')).toBeNull()
    expect(parseColor('var(--primary)')).toBeNull()
    expect(parseColor('red')).toBeNull()
    expect(parseColor('0.375rem')).toBeNull()
    expect(parseColor('none')).toBeNull()
  })

  it('wcagContrast 命中 WCAG 2.x 参考值', () => {
    expect(wcagContrast('#000000', '#ffffff')).toBeCloseTo(21, 6)
    expect(wcagContrast('#ffffff', '#000000')).toBeCloseTo(21, 6)
    expect(wcagContrast('#7c86ff', '#7c86ff')).toBeCloseTo(1, 10)
    // 经典分界：同是中性灰，一深一浅跨在 4.5:1 两侧
    expect(wcagContrast('#767676', '#ffffff')).toBeCloseTo(4.54, 2)
    expect(wcagContrast('#777777', '#ffffff')).toBeCloseTo(4.48, 2)
  })

  it('OKLCH → sRGB 走标准转换：sRGB 原色往返一致', () => {
    // 参考 OKLCH 取自 OKLab 原始推导里的 sRGB 原色坐标（非本仓库代码生成），
    // 用它反向验证矩阵与 gamma 的正确性；8bit 量化允许 ±1 通道误差。
    const expectClose = (actual: string, expected: string) => {
      const a = parseColor(actual)
      const e = parseColor(expected)
      expect(a).not.toBeNull()
      expect(e).not.toBeNull()
      expect([a!.r - e!.r, a!.g - e!.g, a!.b - e!.b].map(Math.abs)).toEqual([0, 0, 0])
    }

    expect(toHex('oklch(1 0 0)')).toBe('#ffffff')
    expect(toHex('oklch(0 0 0)')).toBe('#000000')
    expectClose('oklch(0.627955 0.257683 29.234)', '#ff0000')
    expectClose('oklch(0.86644 0.294827 142.495)', '#00ff00')
    expectClose('oklch(0.452014 0.313214 264.052)', '#0000ff')
    // 中性灰：OKLab 的 L=0.6 对应 sRGB 中灰
    expectClose('oklch(0.6 0 0)', '#808080')
  })

  it('未解析成功的值不会被静默算成 NaN', () => {
    expect(() => wcagContrast('color-mix(in srgb, #fff 50%, #000)', '#ffffff')).toThrow(/无法解析颜色值/)
  })
})

// ───────────────────────── CSS 解析器的用例 ─────────────────────────

describe('CSS 主题变量解析', () => {
  it('识别 :root / .dark / [data-theme] / [data-theme].dark 四类选择器', () => {
    expect(classifySelector(':root')?.kind).toBe('light')
    expect(classifySelector('.dark')?.kind).toBe('dark')
    expect(classifySelector('[data-theme="teal"]')).toEqual({ kind: 'themeLight', theme: 'teal' })
    expect(classifySelector("[data-theme='ocean'].dark")).toEqual({ kind: 'themeDark', theme: 'ocean' })
    expect(classifySelector('html[data-theme="x"]')).toBeNull()
    expect(classifySelector('.jm-console-shell')).toBeNull()
  })

  it('注释被剔除，不会污染声明解析', () => {
    const sheet = parseStylesheet(
      ':root {\n  /* --primary: #000000; 这是注释里的假声明 */\n  --primary: #158053;\n}',
      'synthetic.css',
    )
    expect(sheet.light.get('--primary')).toBe('#158053')
    expect(sheet.light.size).toBe(1)
  })

  it('亮色合并 = 基线 + 主题块；暗色合并 = 基线 + .dark + 主题块 + 主题暗色块', () => {
    const tokens = parseStylesheet(':root { --a: #111111; --b: #222222; }\n.dark { --a: #333333; }', 'synthetic.css')
    const themes = parseStylesheet(
      '[data-theme="t"] { --a: #444444; --c: #555555; }\n[data-theme="t"].dark { --a: #666666; }',
      'synthetic.css',
    )

    // 亮色：主题块覆盖面大于 :root
    expect(cascadeVars(tokens, themes, 't', 'light').get('--a')).toBe('#444444')
    expect(cascadeVars(tokens, themes, 't', 'light').get('--b')).toBe('#222222')
    // 暗色：[data-theme="t"].dark 特异性最高，压过 .dark 与亮色主题块
    expect(cascadeVars(tokens, themes, 't', 'dark').get('--a')).toBe('#666666')
    // 暗色里未被主题块覆盖的项仍来自 .dark
    expect(cascadeVars(tokens, themes, 't', 'dark').get('--b')).toBe('#222222')
    // 默认主题没有主题块，只有基线与 .dark
    expect(cascadeVars(tokens, themes, 'indigo', 'light').get('--a')).toBe('#111111')
    expect(cascadeVars(tokens, themes, 'indigo', 'dark').get('--a')).toBe('#333333')
  })
})

// ───────────────────────── 防退化：结构与品牌变量齐备 ─────────────────────────

/** 结构色 12 项：每套主题块都必须自行声明，避免依赖基线继承导致观感与主题色温不符。 */
const STRUCTURAL_TOKENS = [
  '--background',
  '--foreground',
  '--card',
  '--card-foreground',
  '--popover',
  '--popover-foreground',
  '--secondary',
  '--secondary-foreground',
  '--muted',
  '--muted-foreground',
  '--border',
  '--input',
]

/** 品牌 7 变量：决定主操作与晕染色，同样必须逐主题声明。 */
const BRAND_TOKENS = [
  '--primary',
  '--primary-foreground',
  '--accent',
  '--accent-foreground',
  '--ring',
  '--brand-shadow',
  '--chart-1',
]

describe('主题 token 覆盖（防新增主题漏定义）', () => {
  it('两套样式表里所有选择器都被解析器识别（没有静默丢弃的作用域）', () => {
    expect(TOKENS.unknownSelectors).toEqual([])
    expect(THEMES.unknownSelectors).toEqual([])
  })

  it('至少包含预期的 5 套主题，且默认主题 indigo 不写 data-theme 块', () => {
    expect(THEME_NAMES).toEqual(expect.arrayContaining(['indigo', 'teal', 'ocean', 'violet', 'sunset']))
    expect(THEMES.themeLight.has(DEFAULT_THEME)).toBe(false)
    expect(THEMES.themeDark.has(DEFAULT_THEME)).toBe(false)
  })

  it.each(THEME_NAMES.filter((theme) => theme !== DEFAULT_THEME))('%s 的亮色块声明了结构 12 项与品牌 7 项', (theme) => {
    const block = THEMES.themeLight.get(theme)
    expect(block, `${theme} 缺少 [data-theme="${theme}"] 亮色块`).toBeTruthy()
    const missing = [...STRUCTURAL_TOKENS, ...BRAND_TOKENS].filter((name) => !block?.has(name))
    expect(missing, `${theme} 亮色块漏定义：${missing.join(', ')}`).toEqual([])
  })

  it.each(THEME_NAMES.filter((theme) => theme !== DEFAULT_THEME))('%s 的暗色块声明了结构 12 项与品牌 7 项', (theme) => {
    const block = THEMES.themeDark.get(theme)
    expect(block, `${theme} 缺少 [data-theme="${theme}"].dark 暗色块`).toBeTruthy()
    const missing = [...STRUCTURAL_TOKENS, ...BRAND_TOKENS].filter((name) => !block?.has(name))
    expect(missing, `${theme} 暗色块漏定义：${missing.join(', ')}`).toEqual([])
  })

  it('默认主题 indigo 依赖基线，:root 与 .dark 同样声明齐备', () => {
    const lightMissing = [...STRUCTURAL_TOKENS, ...BRAND_TOKENS].filter((name) => !TOKENS.light.has(name))
    const darkMissing = STRUCTURAL_TOKENS.filter((name) => !TOKENS.dark.has(name))
    expect(lightMissing, `:root 漏定义：${lightMissing.join(', ')}`).toEqual([])
    expect(darkMissing, `.dark 漏定义：${darkMissing.join(', ')}`).toEqual([])
  })

  it('合并作用域覆盖了项目使用的四种颜色写法，且全部可解析', () => {
    const allValues = MATRIX.flatMap((cell) => [...cell.vars.values()])
    const colorValues = allValues.filter((value) => parseColor(value) !== null)

    expect(colorValues.some((value) => /^#/.test(value)), '缺少 hex 写法的取值').toBe(true)
    expect(colorValues.some((value) => /^oklch\(/.test(value)), '缺少 oklch 写法的取值').toBe(true)
    expect(colorValues.some((value) => /^\d+\s+\d+\s+\d+$/.test(value)), '缺少裸分量三元组的取值').toBe(true)

    // 看起来是颜色却解析不出来的值——要么新写法需要补解析器，要么值本身写错了
    const unparsed = allValues.filter(
      (value) => /^(#|oklch\(|rgba?\(|hsla?\(|lab\(|lch\(|color\()/i.test(value) && parseColor(value) === null,
    )
    expect(Array.from(new Set(unparsed)), `有颜色值无法解析：${unparsed.join(' | ')}`).toEqual([])
  })
})

// ───────────────────────── 对比度矩阵：5 主题 × 2 明暗 × 11 配对 ─────────────────────────

/**
 * 用例名用 %s 打印预先拼好的标题，于是失败信息里能一眼定位到
 * 「哪套主题 · 哪个明暗 · 哪一对变量」，不必再翻详情或猜测试数据。
 */
const THEME_CELL_CASES = MATRIX.map(
  (cell) => [`${cell.label} 主题配色对比度`, cell] as const,
)

const PAIR_CASES = CONTRAST_PAIRS.map(
  (pair) => [`${pair.fg} on ${pair.bg} ≥ ${pair.minRatio}:1（${pair.usage}）`, pair] as const,
)

describe.each(THEME_CELL_CASES)('%s', (_cellLabel, cell) => {
  it.each(PAIR_CASES)('%s', (_pairLabel, pair) => {
    const where = cell.label
    const fgValue = requireVar(cell.vars, pair.fg, where)
    const bgValue = requireVar(cell.vars, pair.bg, where)
    const ratio = wcagContrast(fgValue, bgValue)

    expect(
      ratio,
      `${where} ${pair.fg}(${fgValue} → ${toHex(fgValue)}) on ${pair.bg}(${bgValue} → ${toHex(bgValue)}) ` +
        `实测 ${ratio.toFixed(2)}:1 < 要求 ${pair.minRatio}:1（用途：${pair.usage}）`,
    ).toBeGreaterThanOrEqual(pair.minRatio - 1e-6)
  })
})

describe('对比度总览', () => {
  it('10 组配色 × 11 对配平全部达标（失败时一次性列出全部不达标项）', () => {
    const violations: string[] = []

    for (const cell of MATRIX) {
      for (const pair of CONTRAST_PAIRS) {
        const fgValue = cell.vars.get(pair.fg)
        const bgValue = cell.vars.get(pair.bg)
        if (!fgValue || !bgValue) {
          violations.push(`${cell.theme}·${cell.mode} 缺少 ${fgValue ? pair.bg : pair.fg} 定义`)
          continue
        }
        if (!meetsContrast(fgValue, bgValue, pair.minRatio)) {
          const ratio = wcagContrast(fgValue, bgValue)
          violations.push(
            `${cell.theme}·${cell.mode} ${pair.fg} on ${pair.bg} = ${ratio.toFixed(2)}:1（要求 ≥ ${pair.minRatio}:1，用途：${pair.usage}）`,
          )
        }
      }
    }

    expect(
      violations,
      `共 ${MATRIX.length * CONTRAST_PAIRS.length} 项断言，不达标 ${violations.length} 项：\n${violations.join('\n')}`,
    ).toEqual([])
  })
})
