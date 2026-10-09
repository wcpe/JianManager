import { describe, expect, it, vi } from 'vitest'

import { CONSOLE_BUFFER_LIMIT, ConsoleLineBuffer } from './console-line-buffer'

/** 取快照的正文数组（断言用）。 */
const bodies = (buffer: ConsoleLineBuffer) => buffer.snapshot().map((line) => line.body)

describe('ConsoleLineBuffer 分行', () => {
  it('按 \\n 落行，未终止的尾段作为预览行出现在末尾', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('first\nsecond\npartial', 'stdout')
    expect(bodies(buffer)).toEqual(['first', 'second', 'partial'])
  })

  it('尾段跨 chunk 拼接：不产生半行，且落定后 seq 与预览时一致', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('Done (12.', 'stdout')
    const previewSeq = buffer.snapshot()[0].seq
    buffer.appendChunk('345s)!\n', 'stdout')
    expect(bodies(buffer)).toEqual(['Done (12.345s)!'])
    // 预览与落定必须同一个 seq——否则 FR-418 的选区锚点会指向一个消失的行。
    expect(buffer.snapshot()[0].seq).toBe(previewSeq)
  })

  it('\\r\\n 与裸 \\r 都断行（ADR-086 代价 5：不做原地覆盖）', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('a\r\nb\rc\n', 'stdout')
    expect(bodies(buffer)).toEqual(['a', 'b', 'c'])
  })

  it('空行保留（服务端确实输出了空行，不该被吞）', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('a\n\nb\n', 'stdout')
    expect(bodies(buffer)).toEqual(['a', '', 'b'])
  })

  it('流切换时先落定尾段：stdout 半行与 stderr 半行不得拼成一行', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('out-half', 'stdout')
    buffer.appendChunk('err-half\n', 'stderr')
    expect(bodies(buffer)).toEqual(['out-half', 'err-half'])
    expect(buffer.snapshot()[0].level).toBe('INFO')
    expect(buffer.snapshot()[1].level).toBe('ERROR')
  })

  it('seq 单调递增且唯一', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('a\nb\nc\n', 'stdout')
    expect(buffer.snapshot().map((line) => line.seq)).toEqual([0, 1, 2])
  })
})

describe('ConsoleLineBuffer 合成行', () => {
  it('命令回显 kind=command 并进入快照', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('boot\n', 'stdout')
    buffer.appendCommand('say hello')
    const snapshot = buffer.snapshot()
    expect(snapshot.at(-1)).toMatchObject({ kind: 'command', body: 'say hello', raw: 'say hello' })
  })

  it('合成行先落定尾段，顺序不被打乱', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('half-line', 'stdout')
    buffer.appendCommand('stop')
    expect(bodies(buffer)).toEqual(['half-line', 'stop'])
    expect(buffer.snapshot().map((line) => line.seq)).toEqual([0, 1])
  })

  it('系统提示 kind=system', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendSystem('[连接已断开]')
    expect(buffer.snapshot()[0].kind).toBe('system')
  })
})

describe('ConsoleLineBuffer 环形上限（spec §2.1：5000 行，溢出丢最旧）', () => {
  it('上限常量为 5000', () => {
    expect(CONSOLE_BUFFER_LIMIT).toBe(5000)
  })

  it('超出上限丢最旧：长度封顶、首行是第 N+1 行、末行是最新行', () => {
    const buffer = new ConsoleLineBuffer()
    const overflow = 7
    for (let i = 0; i < CONSOLE_BUFFER_LIMIT + overflow; i++) buffer.appendChunk(`line-${i}\n`, 'stdout')

    const snapshot = buffer.snapshot()
    expect(snapshot).toHaveLength(CONSOLE_BUFFER_LIMIT)
    expect(snapshot[0].body).toBe(`line-${overflow}`)
    expect(snapshot.at(-1)!.body).toBe(`line-${CONSOLE_BUFFER_LIMIT + overflow - 1}`)
    // 最旧的那几行真的没了，不是被藏起来。
    expect(snapshot.some((line) => line.body === 'line-0')).toBe(false)
  })

  it('丢弃计数等于溢出行数（顶部回溯锚点据此显示）', () => {
    const buffer = new ConsoleLineBuffer()
    for (let i = 0; i < CONSOLE_BUFFER_LIMIT + 12; i++) buffer.appendChunk(`line-${i}\n`, 'stdout')
    expect(buffer.droppedCount).toBe(12)
  })

  it('未溢出时丢弃计数为 0', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('a\nb\n', 'stdout')
    expect(buffer.droppedCount).toBe(0)
  })

  it('丢弃不回绕 seq：留存行的 seq 仍是其原始序号', () => {
    const buffer = new ConsoleLineBuffer()
    for (let i = 0; i < CONSOLE_BUFFER_LIMIT + 3; i++) buffer.appendChunk(`line-${i}\n`, 'stdout')
    expect(buffer.snapshot()[0].seq).toBe(3)
    expect(buffer.totalSeq).toBe(CONSOLE_BUFFER_LIMIT + 3)
  })

  it('溢出后回溯接缝取最早保留行的接收时刻，而不是会话创建时刻', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-27T00:00:00Z'))
    const buffer = new ConsoleLineBuffer()
    vi.setSystemTime(new Date('2026-08-27T00:05:00Z'))
    for (let i = 0; i < CONSOLE_BUFFER_LIMIT; i++) buffer.appendChunk(`line-${i}\n`, 'stdout')
    vi.setSystemTime(new Date('2026-08-27T00:06:00Z'))
    buffer.appendChunk('overflow\n', 'stdout')

    expect(buffer.historyAnchorAt.toISOString()).toBe('2026-08-27T00:05:00.000Z')
    vi.useRealTimers()
  })
})

describe('ConsoleLineBuffer 快照引用稳定性（useSyncExternalStore 前提）', () => {
  it('无变更时返回同一引用，有变更后换新引用', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('a\n', 'stdout')
    const first = buffer.snapshot()
    expect(buffer.snapshot()).toBe(first)

    buffer.appendChunk('b\n', 'stdout')
    const second = buffer.snapshot()
    expect(second).not.toBe(first)
    expect(buffer.snapshot()).toBe(second)
  })

  it('空 chunk 不触发变更（引用不变）', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('a\n', 'stdout')
    const first = buffer.snapshot()
    buffer.appendChunk('', 'stdout')
    expect(buffer.snapshot()).toBe(first)
  })
})

describe('ConsoleLineBuffer.clear', () => {
  it('清空行与丢弃计数，但 seq 不回绕（已被引用的 seq 不能指向别的行）', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('a\nb\n', 'stdout')
    buffer.clear()
    expect(buffer.snapshot()).toEqual([])
    expect(buffer.droppedCount).toBe(0)

    buffer.appendChunk('c\n', 'stdout')
    expect(buffer.snapshot()[0].seq).toBe(2)
  })
})
