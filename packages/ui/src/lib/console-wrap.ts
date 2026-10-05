/**
 * 控制台自动换行的行数估算（FR-415 可用性增强「自动换行」）。
 *
 * 为什么是估算：行高必须**渲染前**知道，虚拟化才排得出占位。估算用字符分类宽度
 * （等宽 ASCII=1×、CJK/全角=2×、零宽=0）做纯数学，渲染后由 ConsoleOutputView 的
 * 自校正 pass 用真实 scrollHeight 修正——估算只影响未渲染行的滚动条数学，
 * 已渲染行的最终高度永远以实测为准。
 *
 * 纯函数无 DOM 依赖（jsdom 可测）；宽度参数由调用方注入（canvas measureText
 * 或按字号折算的回退值），不在此处碰 canvas。
 */

/** 单字符宽度分类：0=零宽（组合符/变体选择符等），1=普通，2=宽字符（CJK/全角）。 */
export function charWidthClass(code: number): 0 | 1 | 2 {
  // 零宽：组合附加符号、变体选择符、零宽空格/连接符、BOM。
  if (
    (code >= 0x0300 && code <= 0x036f) ||
    (code >= 0x200b && code <= 0x200f) ||
    (code >= 0xfe00 && code <= 0xfe0f) ||
    code === 0xfeff
  ) {
    return 0
  }
  // East Asian Wide / Fullwidth 常用区段（Practical 端子集，覆盖日志里实际出现的文字）。
  if (
    (code >= 0x1100 && code <= 0x115f) || // Hangul Jamo
    (code >= 0x2e80 && code <= 0x303e) || // 部首/康熙/CJK 符号
    (code >= 0x3041 && code <= 0x33ff) || // 平假名/片假名/注音/兼容
    (code >= 0x3400 && code <= 0x4dbf) || // CJK 扩展 A
    (code >= 0x4e00 && code <= 0x9fff) || // CJK 基本区
    (code >= 0xa000 && code <= 0xa4cf) || // 彝文
    (code >= 0xac00 && code <= 0xd7a3) || // Hangul 音节
    (code >= 0xf900 && code <= 0xfaff) || // CJK 兼容表意
    (code >= 0xfe30 && code <= 0xfe4f) || // CJK 兼容形式
    (code >= 0xff00 && code <= 0xff60) || // 全角 ASCII/标点
    (code >= 0xffe0 && code <= 0xffe6) || // 全角符号
    (code >= 0x20000 && code <= 0x2fffd) || // CJK 扩展 B-F
    (code >= 0x30000 && code <= 0x3fffd)
  ) {
    return 2
  }
  return 1
}

export interface WrapWidthSpec {
  /** 普通字符宽度（px），等宽字体下即 '0' 的 advance。 */
  charWidthPx: number
  /** 宽字符宽度（px），通常 ≈ 2 × charWidthPx，但按实测注入。 */
  wideWidthPx: number
  /**
   * 安全余量（px）：canvas measureText 与 CSS 布局的亚像素差、字距边缘差，
   * 宁可多估一行（空一条）也不许少估一行（文本被裁掉）。
   */
  safetyPx?: number
}

/** 按宽度预算算一段文本占几行（至少 1 行；预算 ≤0 视为 1 行，交自校正兜底）。 */
export function countTextLines(text: string, availPx: number, spec: WrapWidthSpec): number {
  if (!text) return 1
  if (!(availPx > 0)) return 1
  let width = 0
  for (let i = 0; i < text.length; i++) {
    const cls = charWidthClass(text.charCodeAt(i))
    // 代理对（astral，如 emoji）按一个宽字符计，避免拆成两个半字符。
    if (cls === 0) continue
    width += cls === 2 ? spec.wideWidthPx : spec.charWidthPx
    if (text.charCodeAt(i) >= 0xd800 && text.charCodeAt(i) <= 0xdbff) i++
  }
  const safety = spec.safetyPx ?? 2
  return Math.max(1, Math.ceil((width + safety) / availPx))
}

export interface RowWrapInput {
  /** 行内可见文本（按渲染单元拼接，不含 ANSI 转义序列）。 */
  body: string
  /** 时间戳文本（缺省无此列）。 */
  ts?: string
  /** 级别文本（command/system 行不渲染级别列）。 */
  level?: string
  /** 来源标签文本（渲染时外层再包一对方括号）。 */
  source?: string
  /** 行类别：command 行有 `>` 前缀，stack-frame 有缩进，均占宽。 */
  kind?: string
  /** 堆栈折叠块展开态下头行还有帧数徽标+复制钮，粗略按固定 px 计。 */
  blockDecorPx?: number
  /** 行容器内容宽度（px，已扣横向内边距）。 */
  rowWidthPx: number
  /** 字体规格。 */
  spec: WrapWidthSpec
}

/** 前缀列的固定杂项宽度（px）：gap-2×列间距、px-2 内边距、级别列 w-[3.25rem]。 */
const LEVEL_CELL_PX = 52
const GAP_PX = 8
const PAD_PX = 16

/**
 * 估算一行日志在自动换行模式下占几行（≥1）。
 *
 * 前缀列（时间/级别/来源/command 标记）都是 shrink-0 不换行，只有正文在
 * 剩余宽度里折行——先扣掉前缀宽得到正文预算，再按字符分类数行。
 * 估算偏差交给渲染后的实测自校正兜底，这里不必做到像素级。
 */
export function estimateRowLines(input: RowWrapInput): number {
  const { rowWidthPx, spec } = input
  if (!(rowWidthPx > 0)) return 1

  const textW = (s: string | undefined) => (s ? measureTextWidth(s, spec) : 0)

  let prefix = PAD_PX
  prefix += textW(input.ts) > 0 ? textW(input.ts) + GAP_PX : 0
  const isPlain = input.kind !== 'command' && input.kind !== 'system'
  if (isPlain && input.level) prefix += LEVEL_CELL_PX + GAP_PX
  if (input.source) prefix += textW(`[${input.source}]`) + GAP_PX
  if (input.kind === 'command') prefix += spec.charWidthPx + GAP_PX
  if (input.kind === 'stack-frame') prefix += spec.charWidthPx * 4
  if (input.blockDecorPx) prefix += input.blockDecorPx

  const bodyAvail = rowWidthPx - prefix
  return countTextLines(input.body, bodyAvail, spec)
}

/** 供 estimateRowLines 内部用的纯宽度求和（不走 countTextLines 的 ceil）。 */
function measureTextWidth(text: string, spec: WrapWidthSpec): number {
  let width = 0
  for (let i = 0; i < text.length; i++) {
    const cls = charWidthClass(text.charCodeAt(i))
    if (cls === 0) continue
    width += cls === 2 ? spec.wideWidthPx : spec.charWidthPx
    if (text.charCodeAt(i) >= 0xd800 && text.charCodeAt(i) <= 0xdbff) i++
  }
  return width
}
