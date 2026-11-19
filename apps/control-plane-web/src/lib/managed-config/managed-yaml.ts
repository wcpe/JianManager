/**
 * @file 托管配置的 YAML 编解码（带注释）。
 *
 * ## 为什么是 YAML 而不是 JSON
 * 托管配置本身就是探针 `config.yml` 的增量，运维日常读写的也是 YAML。
 * 之前的高级编辑区用 JSON，等于要求运维在两套语法之间来回翻译，
 * 而 `config.yml` 里本来就有的行内注释也被抹掉。改成 YAML 后，
 * 「页面上看到的」与「探针文件里的」是同一套写法。
 *
 * ## 注释从哪来
 * 注释不是手写常量，而是从 `GET /managed-configs/catalog` 的元数据生成：
 * 每个点分键都有 `title`（中文名）/ `description`（说明）/ `tier`（档位）/
 * `default`（默认值）/ `options`（候选值）/ `min`·`max`（范围）。
 * 这样注释永远和权威目录同步，新增一个键就自动带上注释，不会出现
 * 「文档说这么写、校验说不行」的撕裂。
 *
 * ## 输出的键集合只由配置决定，目录只决定顺序与注释
 * 目录里有、而本层没设置的键**不输出**：本层只保存用户写过的键（增量语义），
 * 自动把所有目录键写出来等于擅自替用户做决定，会把「没设置」变成「显式设为默认」，
 * 从此再也回退不到低层。
 */
import { parse, parseDocument } from 'yaml'

import type { ManagedKeyMeta } from '@/api/managedConfig'

/** YAML 解析结果。 */
export interface YamlParseResult {
  /** 解析成功的对象；失败时为 `null`。 */
  value: Record<string, unknown> | null
  /** 人读错误信息（含位置）；成功时为空串。 */
  error: string
  /** 出错行（1 起）；无错误时为 0。 */
  line: number
  /** 出错列（1 起）；无错误时为 0。 */
  column: number
}

/** 档位中文名（与后端 `ConfigTier` 的 WireName 对齐）。 */
const TIER_LABEL: Record<string, string> = {
  safe: '可改',
  guarded: '危险档',
  forbidden: '禁止档',
}

// ---------------------------------------------------------------------------
// 解析
// ---------------------------------------------------------------------------

/**
 * 解析 YAML 文本为配置对象。
 *
 * 空文本视为「清空该层」（返回空对象），这是合法意图而非错误——
 * 与后端把空配置当作有效增量的口径一致。
 */
export function parseManagedYaml(text: string): YamlParseResult {
  if (!text.trim()) {
    return { value: {}, error: '', line: 0, column: 0 }
  }
  try {
    const parsed = parse(text) as unknown
    if (parsed === null || parsed === undefined) {
      // 全是注释的文档：等价于空配置。
      return { value: {}, error: '', line: 0, column: 0 }
    }
    if (typeof parsed !== 'object' || Array.isArray(parsed)) {
      return {
        value: null,
        error: '配置必须是 YAML 映射（键: 值），不能是列表或单个值',
        line: 1,
        column: 1,
      }
    }
    return { value: parsed as Record<string, unknown>, error: '', line: 0, column: 0 }
  } catch (err) {
    return {
      value: null,
      error: localizeYamlError(err),
      line: errorLine(err),
      column: errorColumn(err),
    }
  }
}

/**
 * 校验 YAML 文本，并额外识别重复键。
 *
 * 与 `parseManagedYaml` 的区别：`parse` 在遇到重复键时直接抛错，错误文案是英文的
 * `Map keys must be unique`；这里统一翻成中文并把行列号摘出来，界面才能
 * 「第 2 行第 5 列：同一层级出现重复键」这样指给用户看。
 */
export function validateManagedYaml(text: string): YamlParseResult {
  const base = parseManagedYaml(text)
  if (base.error || !text.trim()) return base
  try {
    // parse 已成功，parseDocument 只用于拿 warnings（少数版本的重复键只报 warning）。
    const doc = parseDocument(text)
    const warning = doc.warnings.find((w) => w.code === 'DUPLICATE_KEY')
    if (warning) {
      return {
        value: null,
        error: '同一层级出现重复键（后者会覆盖前者，请删掉多余的那处）',
        line: warning.linePos?.[0]?.line ?? 0,
        column: warning.linePos?.[0]?.col ?? 0,
      }
    }
  } catch {
    // parseDocument 与 parse 结果不一致时以 parse 为准（它已经成功过）。
  }
  return base
}

/**
 * 把 `yaml` 的英文报错翻成中文。
 *
 * 不翻译的话，用户看到的是一句英文 + 一大段上下文快照，对「我该改哪」毫无帮助。
 * 只翻译高频的几类（缩进、重复键、Tab、结构错位），其余保留原文而不硬编。
 */
function localizeYamlError(err: unknown): string {
  const message = err instanceof Error ? err.message : String(err)
  // 报错形如 "句子 at line 2, column 5:\n<上下文>\n<箭头>"，只留第一段的句子。
  const firstLine = message.split('\n')[0]?.trim() ?? message
  const sentence = firstLine.replace(/\s+at line \d+, column \d+:?$/, '')

  const DICT: Array<[RegExp, string]> = [
    [/^Map keys must be unique/i, '同一层级出现重复键（后者会覆盖前者，请删掉多余的那处）'],
    [/^All mapping items must start at the same column/i, '同一层的映射项必须从同一列开始（检查缩进）'],
    [/^Nested mappings are not allowed in compact mappings/i, '紧凑写法里不能嵌套映射（改成换行缩进）'],
    [/^Tab characters must not be used in indentation/i, '缩进不能用 Tab，请改成空格'],
    [/^Implicit keys need to be on a single line/i, '键必须写在同一行'],
    [/^End of the stream or a document separator is expected/i, '文档在此处意外结束（可能有未闭合的引号或括号）'],
    [/^Expected .* but found/i, '该处的写法不符合 YAML 语法'],
    [/^Unresolved plain scalar/i, '该处无法解析为普通标量（可能少了引号）'],
    [/^A block sequence may not be used as an implicit map key/i, '块序列不能当作映射的键'],
    [/^Block collection cannot be nested/i, '块集合不能这样嵌套（检查缩进）'],
  ]
  for (const [pattern, zh] of DICT) {
    if (pattern.test(sentence)) return zh
  }
  return sentence || 'YAML 解析失败'
}

/** 从 `yaml` 的 YAMLParseError 上取行号（1 起）。 */
function errorLine(err: unknown): number {
  return (err as { linePos?: Array<{ line: number }> }).linePos?.[0]?.line ?? 0
}

/** 从 `yaml` 的 YAMLParseError 上取列号（1 起）。 */
function errorColumn(err: unknown): number {
  return (err as { linePos?: Array<{ col: number }> }).linePos?.[0]?.col ?? 0
}

// ---------------------------------------------------------------------------
// 序列化（带注释）
// ---------------------------------------------------------------------------

/** 输出树的一个节点：要么是带注释的叶子，要么是分支。 */
interface OutNode {
  key: string
  /** 注释行（不含 `#`，渲染时按缩进补上）。 */
  comment: string[]
  /** 叶子值；分支时为 undefined。 */
  value?: unknown
  /** 分支的子节点；叶子时为 undefined。 */
  children?: OutNode[]
  isLeaf: boolean
}

/** 单个键的注释内容（由目录元数据拼出）。 */
function commentLinesFor(meta: ManagedKeyMeta): string[] {
  const lines: string[] = []
  // 第一行固定是中文名 + 档位，扫一眼就能判断「这个能不能改」。
  lines.push(`${meta.title}（${TIER_LABEL[meta.tier] ?? meta.tier}）`)
  if (meta.description.trim()) {
    lines.push(...wrap(meta.description.trim(), 60))
  }
  // 约束单独成行：默认值 / 取值 / 范围。
  // 合并成一行会让最长的「枚举候选」把整行撑到无法阅读。
  const constraints: string[] = []
  if (meta.default !== undefined && meta.default !== null) {
    constraints.push(`默认 ${JSON.stringify(meta.default)}`)
  }
  if (meta.options && meta.options.length > 0) {
    constraints.push(`可选 ${meta.options.join(' | ')}`)
  }
  if (meta.min !== undefined && meta.max !== undefined) {
    constraints.push(`范围 ${meta.min} ~ ${meta.max}`)
  } else if (meta.min !== undefined) {
    constraints.push(`不小于 ${meta.min}`)
  } else if (meta.max !== undefined) {
    constraints.push(`不大于 ${meta.max}`)
  }
  if (meta.unit) constraints.push(`单位 ${meta.unit}`)
  if (constraints.length > 0) lines.push(constraints.join('；'))
  if (meta.tier === 'forbidden') {
    lines.push('禁止远程写入：此键改动会被探针整包拒绝')
  }
  return lines
}

/** 按宽度折行（中文按 2 个字符宽度算，避免长句一行到底）。 */
function wrap(text: string, width: number): string[] {
  const out: string[] = []
  let current = ''
  let currentWidth = 0
  for (const ch of text) {
    const w = ch.charCodeAt(0) > 0x2e80 ? 2 : 1
    if (currentWidth + w > width && current) {
      out.push(current)
      current = ''
      currentWidth = 0
    }
    current += ch
    currentWidth += w
  }
  if (current) out.push(current)
  return out
}

/**
 * 把配置对象序列化成带注释的 YAML 文本。
 *
 * `items` 是权威目录：注释与键顺序按它来，保证每次打开的顺序一致。
 * 目录里没有的键（用户手写的、未来新增的）追加在末尾且不加注释，
 * 而不是被丢掉——静默丢键比不加注释危险得多。
 */
export function stringifyManagedYaml(doc: Record<string, unknown>, items: ManagedKeyMeta[]): string {
  const tree: OutNode[] = []
  /** 已经由目录条目输出的路径（用于把剩余部分单独收尾）。 */
  const covered: string[][] = []

  for (const meta of items) {
    const path = meta.key.split('.')
    const value = getPath(doc, path)
    if (value === undefined) continue
    insertLeaf(tree, path, value, commentLinesFor(meta))
    covered.push(path)
  }

  // 剩余部分：从副本里删掉已覆盖的路径，剩下的就是目录不认识的键。
  const leftovers = deepClone(doc) as Record<string, unknown>
  for (const path of covered) deletePath(leftovers, path)
  const leftoverNodes = treeFromObject(leftovers)

  const out: string[] = []
  renderNodes(out, tree, 0)
  if (leftoverNodes.length > 0) {
    if (out.length > 0) out.push('')
    out.push('# 以下键不在权威目录中（可能是未来新增键或手写项），原样保留')
    renderNodes(out, leftoverNodes, 0)
  }

  const text = out.join('\n')
  return text ? `${text}\n` : ''
}

/** 把纯对象转成无注释的输出树（用于目录未覆盖的部分）。 */
function treeFromObject(obj: Record<string, unknown>): OutNode[] {
  const nodes: OutNode[] = []
  for (const [key, value] of Object.entries(obj)) {
    if (isPlainObject(value) && Object.keys(value).length > 0) {
      nodes.push({ key, comment: [], children: treeFromObject(value as Record<string, unknown>), isLeaf: false })
    } else {
      nodes.push({ key, comment: [], value, isLeaf: true })
    }
  }
  return nodes
}

/**
 * 在树中插入一条叶子路径，缺的父级自动补成分支。
 *
 * 这是多段键（`alert.rules`）能正确输出成 `alert:` → `rules:` 的关键：
 * 早先直接把点分键当单层键写，导致嵌套键全部落进「未覆盖」分支，
 * 既没有注释、缩进也是错的。
 */
function insertLeaf(tree: OutNode[], path: string[], value: unknown, comment: string[]): void {
  let level = tree
  for (let i = 0; i < path.length - 1; i += 1) {
    const seg = path[i]!
    let node = level.find((n) => n.key === seg && !n.isLeaf)
    if (!node) {
      // 同名的叶子已存在（目录里既有 `alert` 又有 `alert.rules` 这种越界情况）：
      // 把它降级成分支并接管它的内容，保证不丢任何一个键。
      const existing = level.find((n) => n.key === seg)
      if (existing) {
        existing.isLeaf = false
        existing.children = isPlainObject(existing.value)
          ? treeFromObject(existing.value as Record<string, unknown>)
          : []
        existing.value = undefined
        node = existing
      } else {
        node = { key: seg, comment: [], children: [], isLeaf: false }
        level.push(node)
      }
    }
    level = node.children!
  }
  const leafKey = path[path.length - 1]!
  const same = level.find((n) => n.key === leafKey)
  if (same) {
    // 同一路径被两条目录项描述（不该发生）：保留先到的注释，内容以最新值为准。
    same.value = value
    same.isLeaf = true
    return
  }
  level.push({ key: leafKey, comment, value, isLeaf: true })
}

/** 渲染一组节点。 */
function renderNodes(out: string[], nodes: OutNode[], indent: number): void {
  const pad = '  '.repeat(indent)
  for (const node of nodes) {
    // 注释与它描述的键同缩进，嵌套时才对得上。
    for (const line of node.comment) out.push(`${pad}# ${line}`)
    if (node.isLeaf) {
      emitEntry(out, pad, node.key, node.value, indent)
    } else {
      out.push(`${pad}${node.key}:`)
      renderNodes(out, node.children ?? [], indent + 1)
    }
  }
}

/** 输出一条 `key: value`（value 可能是嵌套块或块序列）。 */
function emitEntry(out: string[], pad: string, key: string, value: unknown, indent: number): void {
  if (value === null || value === undefined) {
    out.push(`${pad}${key}: null`)
    return
  }
  if (Array.isArray(value)) {
    if (value.length === 0) {
      out.push(`${pad}${key}: []`)
      return
    }
    out.push(`${pad}${key}:`)
    const childPad = `${pad}  `
    for (const item of value) {
      if (item === null || typeof item !== 'object' || Array.isArray(item)) {
        out.push(`${childPad}- ${scalarToYaml(item)}`)
        continue
      }
      const entries = Object.entries(item as Record<string, unknown>)
      if (entries.length === 0) {
        out.push(`${childPad}- {}`)
        continue
      }
      // 对象数组（如 alert.rules 的规则列表）：首项与 `-` 同行，符合 YAML 惯例。
      let first = true
      for (const [k, child] of entries) {
        const prefix = first ? `${childPad}- ` : `${childPad}  `
        if (isPlainObject(child) && Object.keys(child).length > 0) {
          out.push(`${prefix}${k}:`)
          renderNodes(out, treeFromObject(child as Record<string, unknown>), indent + 2)
        } else {
          out.push(`${prefix}${k}: ${Array.isArray(child) ? '[]' : scalarToYaml(child)}`)
        }
        first = false
      }
    }
    return
  }
  if (isPlainObject(value)) {
    const entries = Object.entries(value as Record<string, unknown>)
    if (entries.length === 0) {
      out.push(`${pad}${key}: {}`)
      return
    }
    out.push(`${pad}${key}:`)
    renderNodes(out, treeFromObject(value as Record<string, unknown>), indent + 1)
    return
  }
  out.push(`${pad}${key}: ${scalarToYaml(value)}`)
}

/**
 * 标量转 YAML 字面量。
 *
 * 字符串**一律加引号**：配置里有大量 `1s` / `on` / `yes` / `null` 这类会被 YAML
 * 隐式转型的值（`on` 会变成布尔真），不加引号等于悄悄改了配置的类型。
 */
function scalarToYaml(value: unknown): string {
  if (value === null || value === undefined) return 'null'
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : 'null'
  return JSON.stringify(String(value))
}

// ---------------------------------------------------------------------------
// 对象工具
// ---------------------------------------------------------------------------

/** 按路径段取值。 */
function getPath(doc: Record<string, unknown>, path: string[]): unknown {
  let cur: unknown = doc
  for (const seg of path) {
    if (cur === null || typeof cur !== 'object') return undefined
    cur = (cur as Record<string, unknown>)[seg]
    if (cur === undefined) return undefined
  }
  return cur
}

/** 按路径段删值（父级空了就一起删掉，避免留下 `alert: {}` 这种残壳）。 */
function deletePath(doc: Record<string, unknown>, path: string[]): void {
  const stack: Array<Record<string, unknown>> = [doc]
  let cur: Record<string, unknown> = doc
  for (let i = 0; i < path.length - 1; i += 1) {
    const next = cur[path[i]!]
    if (next === null || typeof next !== 'object' || Array.isArray(next)) return
    cur = next as Record<string, unknown>
    stack.push(cur)
  }
  delete cur[path[path.length - 1]!]
  // 自底向上清理空对象。
  for (let i = stack.length - 1; i > 0; i -= 1) {
    const node = stack[i]!
    if (Object.keys(node).length > 0) break
    const parent = stack[i - 1]!
    const key = path[i - 1]!
    delete parent[key]
  }
}

/** 深拷贝配置（只处理 JSON 能表达的形态，配置里不会有别的）。 */
function deepClone(value: unknown): unknown {
  if (value === null || typeof value !== 'object') return value
  if (Array.isArray(value)) return value.map(deepClone)
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(value as Record<string, unknown>)) out[k] = deepClone(v)
  return out
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}
