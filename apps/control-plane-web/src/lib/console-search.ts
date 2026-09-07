import { stripAnsi } from './console-ansi'
import type { LogLine } from './console-log-line'

/**
 * 控制台输出搜索（FR-415：把既有 xterm 搜索平移到行模型上）。
 *
 * 搜索域 = **屏幕上可见的那几列**（时间 / 级别 / 来源 / 正文），不是 `raw`。
 * 这样「命中数」与「高亮数」由构造保证一致：若按 `raw` 计数，`] [` 之类只存在于
 * 原始前缀里的片段会算进计数却无处可高亮，用户看到「3 项」却只见 1 处黄底。
 *
 * 级别过滤与高亮样式的深化属 FR-417，此处只保证能力不退。
 */

/** 行内的可见列下标（与渲染顺序一致）。 */
export const CONSOLE_CELL_TS = 0
export const CONSOLE_CELL_LEVEL = 1
export const CONSOLE_CELL_SOURCE = 2
export const CONSOLE_CELL_BODY = 3

/** 一处命中。`start`/`end` 是该列**纯文本**内的下标（与 ANSI 片段拼接后的文本对齐）。 */
export interface ConsoleSearchMatch {
  seq: number
  /** 在传入行数组中的下标，供虚拟列表定位。 */
  lineIndex: number
  cell: number
  start: number
  end: number
}

/**
 * 行的可见列纯文本。
 *
 * 正文用 {@link stripAnsi}：它与 `parseAnsi` 剥离的字符完全相同，故命中下标可以直接
 * 落到渲染出的片段上，不需要第二套偏移换算。
 */
export function consoleLineCells(line: LogLine): string[] {
  return [line.ts ?? '', line.level ?? '', line.source ?? '', stripAnsi(line.body)]
}

/** 在行数组中找出全部命中，按（行 → 列 → 列内位置）排序。 */
export function findConsoleMatches(lines: readonly LogLine[], query: string): ConsoleSearchMatch[] {
  const needle = query.trim().toLocaleLowerCase()
  if (!needle) return []

  const matches: ConsoleSearchMatch[] = []
  for (let lineIndex = 0; lineIndex < lines.length; lineIndex++) {
    const line = lines[lineIndex]
    const cells = consoleLineCells(line)
    for (let cell = 0; cell < cells.length; cell++) {
      const haystack = cells[cell].toLocaleLowerCase()
      if (!haystack) continue
      let at = haystack.indexOf(needle)
      while (at >= 0) {
        matches.push({ seq: line.seq, lineIndex, cell, start: at, end: at + needle.length })
        at = haystack.indexOf(needle, at + needle.length)
      }
    }
  }
  return matches
}

/** 某行某列上的命中，按列内位置升序（渲染时按此切片加 `<mark>`）。 */
export function matchesForCell(
  matches: readonly ConsoleSearchMatch[],
  seq: number,
  cell: number,
): ConsoleSearchMatch[] {
  return matches.filter((match) => match.seq === seq && match.cell === cell)
}

/** 两处命中是否同一处（判定「当前项」用）。 */
export function isSameMatch(a: ConsoleSearchMatch | undefined, b: ConsoleSearchMatch | undefined): boolean {
  if (!a || !b) return false
  return a.seq === b.seq && a.cell === b.cell && a.start === b.start && a.end === b.end
}
