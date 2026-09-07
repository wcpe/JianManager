import { describe, expect, it } from 'vitest'

import { filterConsoleLines } from './console-filter'
import { ConsoleLineBuffer } from './console-line-buffer'
import type { LogLine } from './console-log-line'
import { buildStackBlocks, stackBlockSize, stackBlockText, visibleConsoleRows } from './console-stack-block'

/**
 * 异常堆栈块（FR-418，spec §3.2）：归并 / 折叠 / 复制整块 / 与级别过滤的接缝。
 */

const NPE = [
  '[09:12:13] [Server thread/ERROR]: java.lang.NullPointerException: boom',
  '\tat a.B.c(B.java:1)',
  '\tat d.E.f(E.java:2)',
  'Caused by: java.lang.IllegalStateException: root',
  '\tat g.H.i(H.java:3)',
  '\t... 3 more',
]

/** 一条普通行 + 一个完整 NPE 块 + 一条普通行（块头 seq=1，帧 seq=2..6）。 */
function makeStackBuffer(): readonly LogLine[] {
  const buffer = new ConsoleLineBuffer()
  buffer.appendChunk('[09:12:12] [Server thread/INFO]: Done (3.1s)! For help, type "help"\n', 'stdout')
  buffer.appendChunk(`${NPE.join('\n')}\n`, 'stdout')
  buffer.appendChunk('[09:12:14] [Server thread/INFO]: after\n', 'stdout')
  return buffer.snapshot()
}

describe('buildStackBlocks', () => {
  it('异常头 + 全部帧 + Caused by 链归并为一个块', () => {
    const blocks = buildStackBlocks(makeStackBuffer())

    expect(blocks.size).toBe(1)
    const block = blocks.get(1)!
    expect(block.headSeq).toBe(1)
    expect(block.frameSeqs).toEqual([2, 3, 4, 5, 6])
    expect(block.lastSeq).toBe(6)
    expect(stackBlockSize(block)).toBe(6)
  })

  it('无异常时不产生块，且原样返回入参引用（常态路径零拷贝）', () => {
    const buffer = new ConsoleLineBuffer()
    for (let i = 0; i < 20; i++) buffer.appendChunk(`line-${i}\n`, 'stdout')
    const lines = buffer.snapshot()
    const blocks = buildStackBlocks(lines)

    expect(blocks.size).toBe(0)
    expect(visibleConsoleRows(lines, blocks, new Set())).toBe(lines)
  })

  it('头行已被环形缓冲丢弃时不成块：孤儿帧按普通行渲染，不藏进看不见的抽屉', () => {
    const lines = makeStackBuffer().slice(2) // 掐掉普通行与异常头，只留帧
    const blocks = buildStackBlocks(lines)

    expect(blocks.size).toBe(0)
    expect(visibleConsoleRows(lines, blocks, new Set())).toHaveLength(lines.length)
  })
})

describe('折叠与展开', () => {
  it('默认折叠：块内的帧不进渲染序列，头行与其他行都在', () => {
    const lines = makeStackBuffer()
    const blocks = buildStackBlocks(lines)
    const rows = visibleConsoleRows(lines, blocks, new Set())

    expect(rows.map((line) => line.seq)).toEqual([0, 1, 7])
  })

  it('展开该块后帧全部回到渲染序列，顺序不变', () => {
    const lines = makeStackBuffer()
    const blocks = buildStackBlocks(lines)
    const rows = visibleConsoleRows(lines, blocks, new Set([1]))

    expect(rows.map((line) => line.seq)).toEqual([0, 1, 2, 3, 4, 5, 6, 7])
  })

  it('多个异常块互不影响：只展开其中一个', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('java.lang.NullPointerException: a\n\tat a.A.a(A.java:1)\n', 'stdout')
    buffer.appendChunk('java.lang.IllegalStateException: b\n\tat b.B.b(B.java:2)\n', 'stdout')
    const lines = buffer.snapshot()
    const blocks = buildStackBlocks(lines)

    expect([...blocks.keys()]).toEqual([0, 2])
    expect(visibleConsoleRows(lines, blocks, new Set([2])).map((line) => line.seq)).toEqual([0, 2, 3])
  })
})

describe('复制整块', () => {
  it('拿到异常头 + 全部帧 + Caused by 链的 raw 拼接', () => {
    const lines = makeStackBuffer()
    const block = buildStackBlocks(lines).get(1)!

    expect(stackBlockText(lines, block)).toBe(NPE.join('\n'))
  })

  it('不夹带块前后的普通行', () => {
    const lines = makeStackBuffer()
    const text = stackBlockText(lines, buildStackBlocks(lines).get(1)!)

    expect(text).not.toContain('Done (3.1s)')
    expect(text).not.toContain('after')
  })
})

describe('与级别过滤的接缝（spec §3.2：整块作为一个单位）', () => {
  it('切 ERROR 后堆栈块整块保留，不会只剩头行', () => {
    const lines = makeStackBuffer()
    // 帧行解析不出级别、被兜底成 INFO，朴素按级别过滤会把它们全滤掉——
    // filterConsoleLines（FR-417）让帧跟随其 stackOf 头行，此处正是验这条。
    const filtered = filterConsoleLines(lines, 'error')

    expect(filtered.map((line) => line.seq)).toEqual([1, 2, 3, 4, 5, 6])
    // 过滤后的行数组仍能归并成同一个块，折叠层与过滤层正交。
    const blocks = buildStackBlocks(filtered)
    expect(blocks.get(1)!.frameSeqs).toEqual([2, 3, 4, 5, 6])
    expect(visibleConsoleRows(filtered, blocks, new Set()).map((line) => line.seq)).toEqual([1])
    expect(stackBlockText(filtered, blocks.get(1)!)).toBe(NPE.join('\n'))
  })

  it('头行被过滤掉时帧不落单残留（不出现「没有头的一串 at …」）', () => {
    const buffer = new ConsoleLineBuffer()
    // WARN 级异常头 + 帧：切 ERROR 档时整块都该消失。
    buffer.appendChunk('[09:12:13] [Server thread/WARN]: java.lang.IllegalStateException: soft\n', 'stdout')
    buffer.appendChunk('\tat a.B.c(B.java:1)\n', 'stdout')
    buffer.appendChunk('[09:12:14] [Server thread/ERROR]: real failure\n', 'stdout')
    const lines = buffer.snapshot()

    expect(filterConsoleLines(lines, 'error').map((line) => line.seq)).toEqual([2])
  })
})
