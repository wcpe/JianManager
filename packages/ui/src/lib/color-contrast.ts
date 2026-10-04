/**
 * 主题配色对比度工具（FR-496 阶段 2）。
 *
 * ── 为什么需要它 ──
 * 全站有 5 套主题 × 明暗 = 10 组配色，全部定义在 styles/tokens.css 与 styles/themes.css。
 * 在此之前，这些配色的 WCAG 对比度**全靠人工验算**：themes.css 里至今留着
 * 「近白对 oklch(0.704) 青绿仅 2.39:1，不达 4.5:1；深青墨实测 7.18:1」这类手算记录。
 * 手算有两个致命问题：
 *   1. 不可持续：调一个 --primary 就要重算它到 --primary-foreground 的相对亮度，
 *      而实际上没人会在每次改色后重算 6 对以上的组合；
 *   2. 没有闸门：即使算错了、或新主题漏配了一对，也没有任何机制在合并前拦住它——
 *      「按钮文字看不清」只能等用户肉眼发现。
 *
 * 本模块提供最小且可测的底座：解析项目实际使用的颜色写法 + 计算 WCAG 2.x 对比度。
 * 真正的守护在 color-contrast.test.ts：它**直接读 CSS 文件**取值（不在测试里重抄颜色），
 * 于是任何颜色改动都会自动过一遍阈值断言，对比度劣化在 CI 就会变红。
 *
 * ── 设计约束 ──
 * - 纯函数、零依赖、不碰 DOM 与 node API：浏览器与测试环境跑同一份实现。
 * - OKLCH → sRGB 走标准链路（OKLCH → OKLab → linear sRGB → sRGB gamma），
 *   不用 HSL 之类的近似替代——近似值算出来的对比度在青绿/橙这类高饱和色上会明显偏差，
 *   恰好就是最容易出问题的那一批颜色。
 * - 通道按 8bit 量化（四舍五入到整数）后再算亮度：与浏览器把 oklch 落到屏幕色的精度一致，
 *   也让 themes.css 里既有的手算记录可复现。
 */

/** sRGB 三通道，0–255 整数。 */
export interface Rgb {
  r: number
  g: number
  b: number
}

/** OKLCH 三分量：l 归一到 0–1，c 为色度（0 起），h 为色相角（度）。 */
export interface Oklch {
  l: number
  c: number
  h: number
}

/**
 * 可参与对比度计算的颜色输入：CSS 颜色文本，或已解析好的 RGB。
 * 文本形式支持项目实际用到的四种写法（见 parseColor）。
 */
export type ColorInput = string | Rgb

/** 以 10 为底的幂等浮动比较容差：避免浮点误差把恰好达标的配色误判为失败。 */
const RATIO_EPSILON = 1e-6

// ────────────────────────────── 解析 ──────────────────────────────

/** `#rgb` / `#rrggbb`（也接受 `#rgba` / `#rrggbbaa`，alpha 被忽略，理由见下）。 */
const HEX_PATTERN = /^#([0-9a-f]{3,8})$/i
/** `oklch(L C H)`，L 可为 0.704 或 70.4%，H 可带 deg/rad/grad/turn，尾部可带 `/ alpha`。 */
const OKLCH_PATTERN = /^oklch\(\s*([^)]*?)\s*\)$/i
/** `rgb(r g b)` / `rgb(r, g, b)` / `rgba(r, g, b, a)`，分量可为 0–255 数字或百分比。 */
const RGB_PATTERN = /^rgba?\(\s*([^)]*?)\s*\)$/i
/** 裸分量三元组 `20 184 166`——`--brand-shadow` / `--shadow-color` 的真实写法。 */
const BARE_TRIPLET_PATTERN = /^(\d{1,3})\s+(\d{1,3})\s+(\d{1,3})$/

/**
 * 解析单个分量为数字。
 * @param token 分量文本，如 `0.704`、`70.4%`、`183deg`
 * @param percentScale 百分比换算基准（rgb 分量按 255 折算，oklch 的 L 按 1 折算）
 */
function parseComponent(token: string, percentScale: number): number | null {
  const text = token.trim().toLowerCase()
  // CSS Color 4 的 `none` 语义等价于 0，容错处理以免整条记录解析失败
  if (text === 'none') return 0
  if (text.endsWith('%')) {
    const pct = Number(text.slice(0, -1))
    return Number.isFinite(pct) ? (pct / 100) * percentScale : null
  }
  const num = Number(text)
  return Number.isFinite(num) ? num : null
}

/** 解析色相角为「度」，支持 CSS 的 deg/rad/grad/turn 单位（缺省为 deg）。 */
function parseAngleDegrees(token: string): number | null {
  const text = token.trim().toLowerCase()
  const value = Number.parseFloat(text)
  if (!Number.isFinite(value)) return null
  if (text.endsWith('turn')) return value * 360
  if (text.endsWith('rad')) return (value * 180) / Math.PI
  if (text.endsWith('grad')) return value * 0.9
  return value
}

/** 把任意数值夹到 0–255 并四舍五入为整数通道值。 */
function quantizeChannel(value: number): number {
  if (Number.isNaN(value)) return 0
  return Math.min(255, Math.max(0, Math.round(value)))
}

/** 组装 RGB，顺带做 8bit 量化与区间夹取。 */
function makeRgb(r: number, g: number, b: number): Rgb {
  return { r: quantizeChannel(r), g: quantizeChannel(g), b: quantizeChannel(b) }
}

/**
 * 解析 oklch() 文本为 OKLCH 分量。
 * L 支持小数（0.704）与百分数（70.4%）；alpha 被忽略（见 parseColor 注释）。
 */
export function parseOklch(value: string): Oklch | null {
  const match = OKLCH_PATTERN.exec(value.trim())
  if (!match) return null

  // 先剥离可选 alpha（`/ 0.5`），对比度校验只关心不透明色
  const [colorPart, alphaPart, ...rest] = match[1].split('/')
  if (rest.length > 0) return null
  if (alphaPart !== undefined && parseComponent(alphaPart, 1) === null) return null

  const parts = colorPart.trim().split(/[\s,]+/).filter(Boolean)
  if (parts.length !== 3) return null

  const l = parseComponent(parts[0], 1)
  // CSS Color 4 里 oklch 色度百分比基准是 0.4（100% 对应 C=0.4）
  const c = parseComponent(parts[1], 0.4)
  const h = parseAngleDegrees(parts[2])
  if (l === null || c === null || h === null) return null

  return { l, c, h }
}

/**
 * 解析 CSS 颜色文本为 RGB；无法识别时返回 null。
 *
 * 支持的写法（均为项目 styles/ 里真实出现的形态）：
 *   hex      `#158053`、`#ffffff`、`#fff`
 *   oklch    `oklch(0.704 0.123 183)`、`oklch(70.4% 0.123 183)`
 *   rgb()    `rgb(20 184 166)`、`rgb(20, 184, 166)`、`rgb(20% 72% 65%)`
 *   裸分量   `20 184 166`（--brand-shadow / --shadow-color 的写法，视作 0–255）
 *
 * 说明：带 alpha 的写法（`#rrggbbaa`、`rgba(..., 0.5)`、`oklch(... / 0.5)`）会被解析出
 * 不透明基色并忽略 alpha——本模块不做背景合成，主题 token 也全是不透明色；
 * 若将来有半透明 token 参与对比度断言，必须先在此处补上合成逻辑。
 */
export function parseColor(value: string): Rgb | null {
  const text = value.trim()

  const hex = HEX_PATTERN.exec(text)
  if (hex) {
    const digits = hex[1]
    if (digits.length === 3 || digits.length === 4) {
      return makeRgb(
        Number.parseInt(digits[0] + digits[0], 16),
        Number.parseInt(digits[1] + digits[1], 16),
        Number.parseInt(digits[2] + digits[2], 16),
      )
    }
    if (digits.length === 6 || digits.length === 8) {
      return makeRgb(
        Number.parseInt(digits.slice(0, 2), 16),
        Number.parseInt(digits.slice(2, 4), 16),
        Number.parseInt(digits.slice(4, 6), 16),
      )
    }
    return null
  }

  if (OKLCH_PATTERN.test(text)) {
    const oklch = parseOklch(text)
    return oklch ? oklchToSrgb(oklch) : null
  }

  const rgbFn = RGB_PATTERN.exec(text)
  if (rgbFn) {
    const [colorPart, alphaPart, ...rest] = rgbFn[1].split('/')
    if (rest.length > 0) return null
    if (alphaPart !== undefined && parseComponent(alphaPart, 1) === null) return null
    const parts = colorPart.trim().split(/[\s,]+/).filter(Boolean)
    // 传统逗号写法允许第 4 个分量是 alpha：`rgba(255, 0, 0, 0.5)`，与 `/ alpha` 等价
    if (parts.length === 4) {
      if (parseComponent(parts[3], 1) === null) return null
      parts.length = 3
    }
    if (parts.length !== 3) return null
    const channels = parts.map((part) => parseComponent(part, 255))
    if (channels.some((channel) => channel === null)) return null
    return makeRgb(channels[0]!, channels[1]!, channels[2]!)
  }

  const bare = BARE_TRIPLET_PATTERN.exec(text)
  if (bare) {
    return makeRgb(Number(bare[1]), Number(bare[2]), Number(bare[3]))
  }

  return null
}

/** 把颜色输入解析成 RGB，失败即抛错——让 CSS 里的笔误立刻现形，而不是静默算出 NaN。 */
export function resolveRgb(color: ColorInput): Rgb {
  if (typeof color !== 'string') return color
  const parsed = parseColor(color)
  if (!parsed) {
    throw new Error(`无法解析颜色值「${color}」：parseColor 只支持 hex / oklch() / rgb() / 裸分量三元组`)
  }
  return parsed
}

// ─────────────────────── OKLCH → sRGB 转换 ───────────────────────

/** sRGB 传输函数的反函数（linear → gamma 编码）。 */
function encodeGamma(channel: number): number {
  return channel <= 0.0031308 ? 12.92 * channel : 1.055 * channel ** (1 / 2.4) - 0.055
}

/**
 * OKLCH → sRGB（标准链路：OKLCH → OKLab → linear sRGB → gamma 编码）。
 * 矩阵系数取自 Björn Ottosson 的 OKLab 原始推导，输出按 8bit 量化并夹到 sRGB 色域。
 */
export function oklchToSrgb(color: Oklch): Rgb {
  const radians = (color.h * Math.PI) / 180
  const a = color.c * Math.cos(radians)
  const b = color.c * Math.sin(radians)

  // OKLab → LMS（立方前的中间量）
  const lPrime = color.l + 0.3963377774 * a + 0.2158037573 * b
  const mPrime = color.l - 0.1055613458 * a - 0.0638541728 * b
  const sPrime = color.l - 0.0894841775 * a - 1.291485548 * b

  const l = lPrime ** 3
  const m = mPrime ** 3
  const s = sPrime ** 3

  // LMS → linear sRGB
  const linearR = 4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s
  const linearG = -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s
  const linearB = -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s

  return makeRgb(encodeGamma(linearR) * 255, encodeGamma(linearG) * 255, encodeGamma(linearB) * 255)
}

// ───────────────────────── 对比度计算 ─────────────────────────

/** 单个 sRGB 通道的线性化（WCAG 2.x 定义的 gamma 解码）。 */
function linearizeChannel(channel: number): number {
  const normalized = channel / 255
  return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4
}

/** WCAG 2.x 相对亮度（0=黑，1=白）。 */
export function relativeLuminance(color: ColorInput): number {
  const { r, g, b } = resolveRgb(color)
  return 0.2126 * linearizeChannel(r) + 0.7152 * linearizeChannel(g) + 0.0722 * linearizeChannel(b)
}

/**
 * WCAG 2.x 对比度，返回 1–21 之间的无量纲比值。
 * 公式：(L亮 + 0.05) / (L暗 + 0.05)，与前景/背景书写顺序无关。
 */
export function wcagContrast(colorA: ColorInput, colorB: ColorInput): number {
  const luminanceA = relativeLuminance(colorA)
  const luminanceB = relativeLuminance(colorB)
  const lighter = Math.max(luminanceA, luminanceB)
  const darker = Math.min(luminanceA, luminanceB)
  return (lighter + 0.05) / (darker + 0.05)
}

/** 断言用的容差比较：浮点误差不应把恰好达标的配色判红。 */
export function meetsContrast(colorA: ColorInput, colorB: ColorInput, minRatio: number): boolean {
  return wcagContrast(colorA, colorB) + RATIO_EPSILON >= minRatio
}

/** 输出 `#rrggbb`，用于失败信息里定位到具体色值。 */
export function toHex(color: ColorInput): string {
  const { r, g, b } = resolveRgb(color)
  return `#${[r, g, b].map((channel) => channel.toString(16).padStart(2, '0')).join('')}`
}
