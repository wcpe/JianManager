/**
 * 控制台 ANSI 解析（FR-415，spec §1.3，ADR-086 代价 1）。
 *
 * **只解析 SGR（`ESC[…m`）**：MC/Paper 实际只用 16 色前景/背景 + 粗体，不需要完整终端仿真。
 * 其余转义序列（光标移动、清屏、OSC 设标题、DCS…）一律**整段吞掉不渲染**——
 * 这是 ADR-086 明确接受的代价 5：宁可少显示，也绝不把转义字节当正文吐成乱码。
 *
 * 之所以自己解析而不复用 xterm：输出区改成 DOM 虚拟列表后没有终端仿真器可依赖，
 * 而按级别过滤 / 堆栈折叠 / 行级复制（FR-417/418）都要求行内是可拆的 span。
 */

/**
 * xterm.js 的默认 16 色调色板（Tango）。
 *
 * 抄这一份而不是另挑配色，是为了满足 ADR-086 的验证项「ANSI 着色与旧 xterm 渲染肉眼一致」：
 * 旧实现只覆写了 background/foreground/cursor，ANSI 索引色用的就是 xterm 默认值。
 */
const ANSI_PALETTE = [
  '#2e3436', '#cc0000', '#4e9a06', '#c4a000', '#3465a4', '#75507b', '#06989a', '#d3d7cf',
  '#555753', '#ef2929', '#8ae234', '#fce94f', '#729fcf', '#ad7fa8', '#34e2e2', '#eeeeec',
] as const

const ESC = '\u001b'
const BEL = '\u0007'

/** 一段同样式的连续文本。`color`/`background` 未定义表示继承输出区默认色。 */
export interface AnsiSegment {
  text: string
  color?: string
  background?: string
  bold?: boolean
}

type AnsiStyle = Omit<AnsiSegment, 'text'>

interface EscapeToken {
  /** 该转义序列占用的字符数（含 ESC 自身）。 */
  length: number
  /** 仅 SGR 有值：`ESC[` 与结尾 `m` 之间的参数串（可能为空串，等价 `0`）。 */
  sgr?: string
}

/**
 * 判断是否为「应当吞掉」的 C0 控制字符。
 *
 * 保留 `\t`：Java 堆栈帧以 `\tat …` 起头，吞掉制表符会让 spec §1.2 的堆栈帧识别失效。
 * 吞掉 `\r`：spec §1.3 明确不支持回车原地覆盖（行拆分已在行缓冲层按 `\r` 断行完成）。
 */
function isSwallowedControl(code: number): boolean {
  if (code === 0x09) return false
  return code < 0x20 || code === 0x7f
}

/**
 * 从 `index`（必须指向 ESC）起读一个转义序列。
 *
 * 序列被 chunk 边界截断（无终止字节）时返回剩余全长——整段吞掉而非把残字节当正文，
 * 否则一条被拆包的着色日志会在屏幕上留下 `[0;32` 这样的垃圾。
 */
function readEscape(text: string, index: number): EscapeToken {
  const next = text[index + 1]
  if (next === undefined) return { length: 1 }

  // CSI：ESC [ 参数字节(0x30-0x3F)* 中间字节(0x20-0x2F)* 终止字节(0x40-0x7E)
  if (next === '[') {
    let i = index + 2
    let params = ''
    while (i < text.length) {
      const code = text.charCodeAt(i)
      if (code >= 0x30 && code <= 0x3f) {
        params += text[i]
        i++
        continue
      }
      if (code >= 0x20 && code <= 0x2f) {
        i++
        continue
      }
      break
    }
    if (i >= text.length) return { length: text.length - index }
    const final = text[i]
    return final === 'm'
      ? { length: i + 1 - index, sgr: params }
      : { length: i + 1 - index }
  }

  // OSC / DCS / SOS / PM / APC：以 BEL 或 ST（ESC \）终止，整段吞掉。
  if (next === ']' || next === 'P' || next === 'X' || next === '^' || next === '_') {
    let i = index + 2
    while (i < text.length) {
      if (text[i] === BEL) return { length: i + 1 - index }
      if (text[i] === ESC && text[i + 1] === '\\') return { length: i + 2 - index }
      i++
    }
    return { length: text.length - index }
  }

  // nF 类转义（ESC + 中间字节(0x20-0x2F)+ + 终止字节）：如字符集切换 `ESC ( B` 共三字节。
  // 必须按规范吃完终止字节，否则会在正文里漏出一个 `B`。
  const nextCode = next.charCodeAt(0)
  if (nextCode >= 0x20 && nextCode <= 0x2f) {
    let i = index + 1
    while (i < text.length && text.charCodeAt(i) >= 0x20 && text.charCodeAt(i) <= 0x2f) i++
    if (i >= text.length) return { length: text.length - index }
    return { length: i + 1 - index }
  }

  // 其余两字符转义（ESC c 全复位、ESC 7 存光标等）：吞掉。
  return { length: 2 }
}

/** 按 SGR 参数串推进样式。未识别的属性（下划线/反显/扩展色）忽略但不破坏后续解析。 */
function applySgr(style: AnsiStyle, params: string): AnsiStyle {
  // `ESC[m` 与 `ESC[;31m` 里的空参数按 ANSI 规范等价于 0。
  const codes = (params === '' ? '0' : params).split(';').map((part) => (part === '' ? 0 : Number(part)))
  let next: AnsiStyle = { ...style }

  for (let i = 0; i < codes.length; i++) {
    const code = codes[i]
    if (!Number.isInteger(code)) continue
    if (code === 0) {
      next = {}
    } else if (code === 1) {
      next.bold = true
    } else if (code === 22) {
      delete next.bold
    } else if (code >= 30 && code <= 37) {
      next.color = ANSI_PALETTE[code - 30]
    } else if (code >= 90 && code <= 97) {
      next.color = ANSI_PALETTE[code - 90 + 8]
    } else if (code === 39) {
      delete next.color
    } else if (code >= 40 && code <= 47) {
      next.background = ANSI_PALETTE[code - 40]
    } else if (code >= 100 && code <= 107) {
      next.background = ANSI_PALETTE[code - 100 + 8]
    } else if (code === 49) {
      delete next.background
    } else if (code === 38 || code === 48) {
      // 扩展色（256 色 / 真彩）：spec §1.3 不要求支持，但**必须吞掉其后续参数**——
      // 否则 `38;5;196` 里的 5 与 196 会被当成独立 SGR 码误读成别的样式。
      if (codes[i + 1] === 5) i += 2
      else if (codes[i + 1] === 2) i += 4
      else i = codes.length
    }
  }
  return next
}

/**
 * 把含 ANSI 的文本拆成同样式片段。
 *
 * 无 ANSI（MC 日志的绝大多数行）走快路径，避免逐字符扫描。
 */
export function parseAnsi(text: string): AnsiSegment[] {
  if (!text) return []
  if (!text.includes(ESC)) {
    const plain = stripControls(text)
    return plain ? [{ text: plain }] : []
  }

  const segments: AnsiSegment[] = []
  let style: AnsiStyle = {}
  let buffer = ''
  const flush = () => {
    if (!buffer) return
    segments.push({ text: buffer, ...style })
    buffer = ''
  }

  let i = 0
  while (i < text.length) {
    if (text[i] === ESC) {
      const token = readEscape(text, i)
      if (token.sgr !== undefined) {
        // 样式切换前先落定已积攒的文本，使其保留切换前的样式。
        flush()
        style = applySgr(style, token.sgr)
      }
      i += token.length
      continue
    }
    if (isSwallowedControl(text.charCodeAt(i))) {
      i++
      continue
    }
    buffer += text[i]
    i++
  }
  flush()
  return segments
}

/** 去掉除 `\t` 外的全部 C0 控制字符（不含 ESC 序列处理，仅供无 ESC 的快路径用）。 */
function stripControls(text: string): string {
  let out = ''
  for (let i = 0; i < text.length; i++) {
    if (!isSwallowedControl(text.charCodeAt(i))) out += text[i]
  }
  return out
}

/** 去掉全部转义序列与控制字符，得到可用于匹配 / 复制的纯文本。 */
export function stripAnsi(text: string): string {
  if (!text) return ''
  if (!text.includes(ESC)) return stripControls(text)
  let out = ''
  let i = 0
  while (i < text.length) {
    if (text[i] === ESC) {
      i += readEscape(text, i).length
      continue
    }
    if (!isSwallowedControl(text.charCodeAt(i))) out += text[i]
    i++
  }
  return out
}

/** {@link ansiPlainText} 结果：纯文本 + 「纯文本第 i 个字符来自原串哪个下标」的映射。 */
export interface AnsiPlainText {
  plain: string
  /** `map[i]` = `plain[i]` 在原串中的下标；供按纯文本位置切回原串（保留 ANSI）用。 */
  map: number[]
}

/**
 * 计算纯文本及其到原串的下标映射。
 *
 * 行解析器需要「在纯文本上匹配前缀，却在原串上切正文」——直接在含 ANSI 的原串上写正则，
 * 会因 Paper 给时间/级别前缀套色而匹配不上；先剥色再按映射切回，两头都不丢。
 */
export function ansiPlainText(raw: string): AnsiPlainText {
  const chars: string[] = []
  const map: number[] = []
  let i = 0
  while (i < raw.length) {
    if (raw[i] === ESC) {
      i += readEscape(raw, i).length
      continue
    }
    if (!isSwallowedControl(raw.charCodeAt(i))) {
      chars.push(raw[i])
      map.push(i)
    }
    i++
  }
  return { plain: chars.join(''), map }
}

/**
 * 按纯文本下标切掉原串前缀，并把前缀里出现过的 SGR 序列**前置保留**。
 *
 * 保留是必要的：若前缀里 `ESC[0;32m` 开了绿色而正文靠它着色，直接砍掉前缀会让正文失色，
 * 违背「与旧 xterm 渲染肉眼一致」。非 SGR 序列不保留（本模块一律不渲染）。
 */
export function ansiSliceFromPlain(raw: string, map: number[], plainIndex: number): string {
  if (plainIndex <= 0) return raw
  const cut = plainIndex < map.length ? map[plainIndex] : raw.length
  let carried = ''
  let i = 0
  while (i < cut) {
    if (raw[i] === ESC) {
      const token = readEscape(raw, i)
      if (token.sgr !== undefined) carried += `${ESC}[${token.sgr}m`
      i += token.length
      continue
    }
    i++
  }
  return carried + raw.slice(cut)
}
