import type { LogLine } from './console-log-line'

/**
 * 异常堆栈块（FR-418，spec §3.2）。
 *
 * 解析器（FR-415）已把 `\tat …` / `Caused by:` 标成 `stack-frame` / `stack-cause`
 * 并用 `stackOf` 指回异常头行的 seq；本模块只做「归并成块 + 折叠」这一层：
 * 一个 NPE 在控制台里常占 30~60 行，默认展开会把有用的日志整屏挤走。
 *
 * 块 = **异常头行 + 其全部帧 + 整条 `Caused by` 链**（解析器让 Caused by 之后的帧
 * 继续挂在同一个头上，所以一个块天然涵盖整条链，不需要在这里再拼一次）。
 */

/** 一个折叠块。 */
export interface StackBlock {
  /** 异常头行的 seq。 */
  headSeq: number
  /** 头行之后的全部帧 / Caused by 行 seq，按出现顺序。 */
  frameSeqs: number[]
  /** 块末行 seq（双击选中整块时的区间终点）。 */
  lastSeq: number
}

/** headSeq → 块。 */
export type StackBlockMap = ReadonlyMap<number, StackBlock>

const EMPTY_BLOCKS: StackBlockMap = new Map()

/**
 * 归并出全部堆栈块。
 *
 * 头行已被环形缓冲丢弃（只剩孤儿帧）时**不成块**：没有头行就没有可折叠的摘要行，
 * 强行折叠等于把这些帧藏进一个看不见的抽屉。此时它们按普通行渲染。
 */
export function buildStackBlocks(lines: readonly LogLine[]): StackBlockMap {
  const present = new Set<number>()
  for (const line of lines) present.add(line.seq)

  let blocks: Map<number, StackBlock> | null = null
  for (const line of lines) {
    const head = line.stackOf
    if (head === undefined || !present.has(head)) continue
    blocks ??= new Map()
    const existing = blocks.get(head)
    if (existing) {
      existing.frameSeqs.push(line.seq)
      existing.lastSeq = line.seq
    } else {
      blocks.set(head, { headSeq: head, frameSeqs: [line.seq], lastSeq: line.seq })
    }
  }
  return blocks ?? EMPTY_BLOCKS
}

/** 「复制整块」的文本：头行 + 全部帧 + Caused by 链，一律取 `raw`（spec §3.2）。 */
export function stackBlockText(lines: readonly LogLine[], block: StackBlock): string {
  const wanted = new Set<number>([block.headSeq, ...block.frameSeqs])
  return lines
    .filter((line) => wanted.has(line.seq))
    .map((line) => line.raw)
    .join('\n')
}

/** 块的总行数（头 + 帧），用于选中整块后的计数展示。 */
export function stackBlockSize(block: StackBlock): number {
  return block.frameSeqs.length + 1
}

/**
 * 渲染用的可视行：折叠态的块只留头行。
 *
 * 没有任何块时**原样返回入参**（引用不变），让「绝大多数控制台没有异常」这条常态路径
 * 不产生一次数组拷贝，也不打断上层 memo。
 */
export function visibleConsoleRows(
  lines: readonly LogLine[],
  blocks: StackBlockMap,
  expanded: ReadonlySet<number>,
): readonly LogLine[] {
  if (blocks.size === 0) return lines
  return lines.filter((line) => {
    const head = line.stackOf
    if (head === undefined || !blocks.has(head)) return true
    return expanded.has(head)
  })
}

/**
 * 与级别过滤的接缝（spec §3.2）：**堆栈帧跟随其异常头行的可见性**。
 *
 * 这条规则**不在本模块实现**——它归 FR-417 的
 * {@link import('./console-filter').filterConsoleLines}，那里已按同一语义处理
 * （帧不单独参与级别判定、一律跟随 `stackOf`）。此处只做说明，避免同一条规则出现两份实现：
 * 帧行解析不出级别会被兜底成 `INFO`，朴素的 `lines.filter(byLevel)` 在「只看 ERROR」时
 * 恰好只留异常头一行、把整条栈滤掉，于是用户看到「报错了但没有栈」。
 *
 * 折叠层（本模块）只认 `stackOf`，因此过滤后的行数组照样能归并成块：
 * 过滤保留整块 → 折叠把块收成一行；过滤滤掉整块 → 无块可折。两层正交。
 */

