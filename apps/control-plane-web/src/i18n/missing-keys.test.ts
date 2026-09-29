import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import zh from './zh.json'
import en from './en.json'

// 全量 i18n key 缺失检测（静态 best-effort）：
// 扫描 src/**/*.{ts,tsx} 中所有静态 i18next 翻译调用的首参字符串字面量，
// 断言每个 key 同时存在于 zh.json 与 en.json。
// 背景：clientDistMonitor.downloadBytesTrend 等 104 个 key 在代码中使用、
// 但语言包从未定义（多数有 defaultValue 中文兜底，英文界面会漏出中文兜底；
// 少数如 downloadBytesTrend / serverConsole.noSeriesYet 无兜底，直接渲染裸 key）。
//
// 提取只认「代码区」：注释、字符串与模板字面量正文、正则字面量正文里的调用形态
// （注释里举例写法、字符串里写说明文案）只是文字，不是真实调用。曾经的教训是
// 扫描器连注释一起扫，注释里写一句「举例：t('某个不存在的键')」就会误报缺键，
// 逼人改写注释绕开——现在由 extractStaticTKeys 的词法屏蔽解决，回归测试见文件末尾。
//
// 已知边界：动态拼接 key（如 t(`mcpActivity.window_${w}`)）无法静态枚举，本测试对它们
// 零覆盖——档位值改名后漏补语言包，这里不会变红，页面会直接渲染裸键。这类键必须由各自
// 页面自己守（可参考 pages/McpActivityPage.dom.test.tsx 的「窗口档位文案的动态 i18n 键」：
// 它按档位值逐项核对 zh/en 是否存在对应键）。不要因为本测试全绿就以为 i18n 已经完整。

const i18nDir = dirname(fileURLToPath(import.meta.url))
const srcDir = join(i18nDir, '..')

function flattenKeys(obj: Record<string, unknown>, prefix = ''): Set<string> {
  const out = new Set<string>()
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object') {
      for (const p of flattenKeys(v as Record<string, unknown>, path)) out.add(p)
    } else {
      out.add(path)
    }
  }
  return out
}

function listSourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...listSourceFiles(full))
    else if (entry.name.endsWith('.ts') || entry.name.endsWith('.tsx')) out.push(full)
  }
  return out
}

// 与仓库 i18n 审计脚本一致的提取规则：t( 或 i18n.t(，首参为单/双引号字面量
const CALL_RE = /(?:^|[^\w.])(?:i18n\.)?\bt(?:<[^>]*>)?\(\s*(['"])([^'"\n]+)\1/g

/** 非代码区区间（半开区间 [start, end)）：落在其中的调用形态只是文字，不是真实调用。 */
type Span = [start: number, end: number]

/**
 * 正则字面量起点判定：紧跟在这些字符之后的 / 才是正则开始，否则是除号。
 * 特意不含 `<`：TSX 里的结束标签（`</TableHead>`）会让 / 紧跟 `<` 之后，误判成正则会
 * 一路吞到下一个 /，把同一行后面的真实调用一起屏蔽掉（真踩过：ArtifactReconcileSection）。
 * 代价是 `a < /re/` 这类写法认不出正则——但认不出只是少登记一段非代码区，不会漏掉真实调用。
 */
const REGEX_PREFIX_CHARS = '(,=:[!&|?{;+-*%~^>'
/** 补充：return / typeof 等关键字之后的 / 同样是正则开始。 */
const REGEX_PREFIX_KEYWORD_RE =
  /\b(?:return|typeof|case|in|of|new|delete|void|instanceof|do|else|yield|await)\s*$/

interface TemplateFrame {
  /** true=正在读模板正文；false=正在读 ${...} 插值里的代码。 */
  body: boolean
  /** 当前模板正文起点。 */
  bodyStart: number
  /** 插值里 { } 的深度，归零说明插值结束、回到模板正文。 */
  brace: number
}

/** 判断 src[i] 处的 / 是否为正则字面量起点（否则按除号处理，不跳正文）。 */
function startsRegexLiteral(src: string, i: number, prev: string): boolean {
  if (prev === '' || REGEX_PREFIX_CHARS.includes(prev)) return true
  return REGEX_PREFIX_KEYWORD_RE.test(src.slice(Math.max(0, i - 32), i))
}

/**
 * 纯字符级扫描（不建 AST），收集源码里的非代码区：
 * 行/块注释、字符串与模板字面量正文、正则字面量正文。
 * 目的只有一个：把注释/字符串里写出来的调用形态与真实调用分开。
 */
function collectNonCodeSpans(src: string): Span[] {
  const spans: Span[] = []
  const stack: TemplateFrame[] = []
  let prev = '' // 代码区里最近一个非空白字符，用于判定 / 是正则还是除号
  let i = 0

  while (i < src.length) {
    const top = stack[stack.length - 1]
    const c = src[i]

    // 模板字面量正文：整段当文字，只找结尾反引号与插值起点
    if (top && top.body) {
      if (c === '\\') {
        i += 2
        continue
      }
      if (c === '`') {
        spans.push([top.bodyStart, i])
        stack.pop()
        i += 1
        prev = '`'
        continue
      }
      if (c === '$' && src[i + 1] === '{') {
        spans.push([top.bodyStart, i])
        top.body = false
        top.brace = 1
        i += 2
        prev = '{'
        continue
      }
      i += 1
      continue
    }

    // 行注释：到行尾
    if (c === '/' && src[i + 1] === '/') {
      const nl = src.indexOf('\n', i)
      const stop = nl < 0 ? src.length : nl
      spans.push([i, stop])
      i = stop
      continue
    }
    // 块注释：到 */
    if (c === '/' && src[i + 1] === '*') {
      const close = src.indexOf('*/', i + 2)
      const stop = close < 0 ? src.length : close + 2
      spans.push([i, stop])
      i = stop
      continue
    }
    // 单/双引号字符串：正文里的 // 不是注释，必须整体越过
    if (c === '"' || c === "'") {
      let j = i + 1
      while (j < src.length && src[j] !== c && src[j] !== '\n') j += src[j] === '\\' ? 2 : 1
      // 只有同一行内找到闭合引号才登记：JSX 文本里的撇号（Don't）会“开”出一段并不存在的
      // 字符串，登记它会连累同一行后面对真实调用的匹配
      if (src[j] === c) spans.push([i + 1, j])
      i = j + 1
      prev = c
      continue
    }
    // 模板字面量起点
    if (c === '`') {
      stack.push({ body: true, bodyStart: i + 1, brace: 0 })
      i += 1
      prev = '`'
      continue
    }
    // 正则字面量：正文里的 \/\/ 不是注释（如 /^https?:\/\//），必须整体越过
    if (c === '/' && startsRegexLiteral(src, i, prev)) {
      let j = i + 1
      let inClass = false
      while (j < src.length && src[j] !== '\n') {
        const d = src[j]
        if (d === '\\') {
          j += 2
          continue
        }
        if (d === '[') inClass = true
        else if (d === ']') inClass = false
        else if (d === '/' && !inClass) break
        j += 1
      }
      if (src[j] === '/') spans.push([i + 1, j]) // 同行未闭合就不登记（同字符串那段的理由）
      i = j + 1
      prev = '/'
      continue
    }
    // 插值里的花括号配对；归零说明插值结束，回到模板正文
    if (top && c === '{') top.brace += 1
    else if (top && c === '}') {
      top.brace -= 1
      if (top.brace <= 0) {
        top.body = true
        top.bodyStart = i + 1
      }
    }
    if (c.trim() !== '') prev = c
    i += 1
  }
  return spans
}

/**
 * 提取源码里的静态 i18n key（纯函数，便于单测）：
 * 先标出非代码区，再在原始源码上跑 CALL_RE，丢弃「被调用标识符 t 落在非代码区」的匹配。
 * 真实调用的 t 位于代码区（它后面的字面量正是首参），因此不会被误跳。
 */
export function extractStaticTKeys(source: string): string[] {
  const spans = collectNonCodeSpans(source)
  const keys: string[] = []
  for (const m of source.matchAll(CALL_RE)) {
    // 匹配前缀里第一个 t 就是被调用的标识符本身（前缀只可能是 ^、单个非 \w. 字符或 i18n.）
    const calleeAt = m.index + m[0].indexOf('t')
    const inText = spans.some(([start, end]) => calleeAt >= start && calleeAt < end)
    if (inText) continue
    if (!keys.includes(m[2])) keys.push(m[2])
  }
  return keys
}

function collectUsedKeys(): Map<string, string[]> {
  const used = new Map<string, string[]>()
  for (const file of listSourceFiles(srcDir)) {
    const text = readFileSync(file, 'utf-8')
    const rel = file.replace(srcDir + '\\', '').replace(srcDir + '/', '')
    for (const key of extractStaticTKeys(text)) {
      const list = used.get(key) ?? []
      if (!list.includes(rel)) list.push(rel)
      used.set(key, list)
    }
  }
  return used
}

describe('i18n 语言包完整性', () => {
  const zhKeys = flattenKeys(zh)
  const enKeys = flattenKeys(en)
  const used = collectUsedKeys()

  it('zh/en 两个语言包 key 集合完全一致', () => {
    const onlyZh = [...zhKeys].filter((k) => !enKeys.has(k))
    const onlyEn = [...enKeys].filter((k) => !zhKeys.has(k))
    expect(onlyZh, '仅在 zh.json 中存在的 key').toEqual([])
    expect(onlyEn, '仅在 en.json 中存在的 key').toEqual([])
  })

  it('代码中使用的每个静态 t() key 在 zh/en 中都有定义', () => {
    const missing = [...used.entries()]
      .filter(([k]) => !zhKeys.has(k) || !enKeys.has(k))
      .map(([k, files]) => `${k}  <-  ${files.join(', ')}`)
    expect(missing, '语言包缺失的 key（zh 或 en）').toEqual([])
  })
})

// 提取器的回归测试。样本本身写在模板字面量里（本文件也在扫描范围内），
// 所以它还顺带自证：本文件自己的字面量不会被扫成 key。
// 每条「ghost.*」都是修复前会被误当成真实 key 的形态，一条都不许提取出来。
const SAMPLE_SOURCE = `
// 行注释里举例写法：t('ghost.in.line.comment')
/* 块注释里举例写法：t('ghost.in.block.comment') */
const label = t('real.key.one')
const doc = "文案里出现 t('ghost.in.string') 只是文字"
const tplText = \`文案里出现 t('ghost.in.template') 只是文字\`
const re = /[t('ghost.in.regex')]/
const tpl = \`前缀 \${t('real.key.two')} 后缀\`
`

describe('静态 key 提取器（extractStaticTKeys）', () => {
  it('注释与字符串/模板/正则正文里的调用形态不算 key，只提取真实调用', () => {
    expect(extractStaticTKeys(SAMPLE_SOURCE)).toEqual(['real.key.one', 'real.key.two'])
  })

  it('真实调用的各种写法都不会被漏掉（含 i18n.t、类型参数、JSX 内调用）', () => {
    const source = [
      "const a = t('real.plain')",
      "const b = i18n.t('real.namespace')",
      "const c = t<string>('real.generic')",
      "const view = <p>{t('real.jsx')}</p>",
    ].join('\n')
    expect(extractStaticTKeys(source)).toEqual([
      'real.plain',
      'real.namespace',
      'real.generic',
      'real.jsx',
    ])
  })
})
