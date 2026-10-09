import type { LogLevel, LogLine } from './console-log-line'

/**
 * 控制台输出的级别过滤与复制取材（FR-417）。
 *
 * 建立在 FR-415 行模型的 `level` / `kind` / `stackOf` 字段上（spec §1.1）——
 * 这正是弃 xterm 换 DOM 行模型换来的东西：canvas 上没有行对象，「只看 ERROR」
 * 与「只复制错误行」都无从下手。
 */

/** 级别过滤档位。三档而非五档：排障只关心「全部 / 有异常 / 只有错」。 */
export type ConsoleLevelFilter = 'all' | 'warn' | 'error'

/** 级别序：过滤按「≥ 阈值」判定，DEBUG/TRACE 同档（都低于 INFO）。 */
const LEVEL_RANK: Record<LogLevel, number> = {
  TRACE: 0,
  DEBUG: 0,
  INFO: 1,
  WARN: 2,
  ERROR: 3,
}

const FILTER_THRESHOLD: Record<ConsoleLevelFilter, number> = {
  all: 0,
  warn: 2,
  error: 3,
}

export interface FilterOptions {
  /**
   * 是否保留上下文行（`command` 命令回显 / `system` 连接与状态提示）。
   *
   * 展示用 true：它们不是日志级别而是上下文（我敲了什么、连接发生了什么），过滤掉会让
   * 剩下的输出无法解读——一条 ERROR 前面若没有「我刚执行了 reload」，根本读不出因果。
   * 「仅复制错误行」用 false：那时用户要的是一段能贴给别人看的纯错误文本。
   */
  keepContext?: boolean
}

function isStackLine(line: LogLine): boolean {
  return line.kind === 'stack-frame' || line.kind === 'stack-cause'
}

/**
 * 每行的**有效级别序**（FR-417 过滤的真正判据）。
 *
 * 直接用 `line.level` 是不够的，因为 Paper 打一条异常时**只有第一行带记录头**：
 *
 * ```
 * [09:00:02] [Server thread/ERROR]: Could not pass event PlayerJoinEvent   ← 有 tsParsed，级别 ERROR
 * java.lang.NullPointerException: boom                                     ← 无 tsParsed（ts 继承 lastTs），级别兜底成 INFO
 * 	at com.foo.Bar.baz(Bar.java:12)                                         ← 堆栈帧，级别兜底成 INFO
 * ```
 *
 * 照 `line.level` 过滤会把后两行当 INFO 滤掉，切 ERROR 后只剩一句「Could not pass event」，
 * 而**帧才是排障要看的东西**。故按行模型自带的线索定有效级别：
 *
 * - `kind:'log'` 且**有 `tsParsed`**（本行自身命中时间前缀 = 自带完整记录头）：
 *   用自身级别，并成为后续续行的继承源。
 *   **不能只看 `ts` 有无**——lastTs 继承让续行也带 ts（spec §1.2 时间继承增补），
 *   只看 ts 会把续行误当成新记录、按自身兜底级别（INFO）参与过滤，
 *   ERROR 抬头后的整块异常（头+帧+Caused by）会被滤丢（评审 P0-2）。
 * - `kind:'log'` 且**无 `tsParsed`**：是上一条记录的续行（异常正文等）→ 继承上一条的有效级别
 * - 堆栈行：跟随 `stackOf` 指向的头（spec §3.2「整块作为一个单位保留」）；
 *   头已被缓冲丢弃的孤儿行退化为自身级别，宁可多留一行也不静默吞掉异常现场
 * - `command` / `system`：无级别，且**打断继承链**（异常正文不该继承一条命令回显的级别）
 */
function effectiveRanks(lines: readonly LogLine[]): Map<number, number> {
  const ranks = new Map<number, number>()
  let carry: number | undefined
  for (const line of lines) {
    if (line.kind === 'command' || line.kind === 'system') {
      carry = undefined
      continue
    }
    const own = line.level ? LEVEL_RANK[line.level] : undefined
    if (isStackLine(line)) {
      const inherited = line.stackOf === undefined ? undefined : ranks.get(line.stackOf)
      ranks.set(line.seq, inherited ?? own ?? 0)
      continue // 堆栈行不改继承源：块结束后续行仍属块前那条记录。
    }
    const rank = line.tsParsed ? (own ?? 0) : (carry ?? own ?? 0)
    ranks.set(line.seq, rank)
    carry = rank
  }
  return ranks
}

/**
 * 按级别过滤行（FR-417）。
 *
 * **堆栈行不单独参与过滤**：`stack-frame` / `stack-cause` 的可见性一律跟随其 `stackOf`
 * 指向的异常头（spec §3.2「整块作为一个单位保留」）。否则切到 ERROR 时一条 NPE 会只剩
 * 头行、帧全没了——而帧才是排障真正要看的东西。FR-418 的堆栈块折叠直接长在这个语义上。
 *
 * 找不到头的孤儿堆栈行（缓冲溢出把头丢了、或命令回显打断了上下文）退化为按自身级别判定，
 * 宁可多留一行也不静默吞掉异常现场。
 */
export function filterConsoleLines(
  lines: readonly LogLine[],
  filter: ConsoleLevelFilter,
  { keepContext = true }: FilterOptions = {},
): readonly LogLine[] {
  if (filter === 'all' && keepContext) return lines
  const threshold = FILTER_THRESHOLD[filter]
  const ranks = effectiveRanks(lines)

  const out: LogLine[] = []
  for (const line of lines) {
    if (line.kind === 'command' || line.kind === 'system') {
      if (keepContext) out.push(line)
      continue
    }
    if ((ranks.get(line.seq) ?? 0) >= threshold) out.push(line)
  }
  return out
}

/** 复制范围（FR-417 复制菜单）。 */
export type ConsoleCopyScope = 'selection' | 'viewport' | 'buffer' | 'errors'

/**
 * 行数组 → 可复制文本。
 *
 * 一律取 `raw`（spec §1.1）：用户要的是原始日志，不是我们拆列重排过的版本——
 * 贴进 issue 或发给别人时，重排过的文本对不上任何日志检索。
 */
export function consoleLinesToText(lines: readonly LogLine[]): string {
  return lines.map((line) => line.raw).join('\n')
}

/**
 * 「仅错误行」的取材：ERROR 级日志行 + 其完整堆栈块，不含命令回显与系统提示。
 * 见 {@link FilterOptions.keepContext} 的理由说明。
 */
export function consoleErrorLines(lines: readonly LogLine[]): readonly LogLine[] {
  return filterConsoleLines(lines, 'error', { keepContext: false })
}
