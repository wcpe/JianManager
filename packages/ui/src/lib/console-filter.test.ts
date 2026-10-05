import { describe, expect, it } from 'vitest'

import { ConsoleLineBuffer } from './console-line-buffer'
import {
  consoleErrorLines,
  consoleLinesToText,
  filterConsoleLines,
} from './console-filter'
import type { LogLine } from './console-log-line'

/**
 * 级别过滤与复制取材（FR-417）。
 *
 * 一律走真实行缓冲造数据（而非手捏 LogLine 字面量）：过滤依赖解析器产出的
 * `level` / `kind` / `stackOf`，手捏对象会让测试和生产解析规则悄悄分叉。
 */

function build(chunks: readonly (readonly [string, 'stdout' | 'stderr'])[]): readonly LogLine[] {
  const buffer = new ConsoleLineBuffer()
  for (const [text, stream] of chunks) buffer.appendChunk(text, stream)
  return buffer.snapshot()
}

const raws = (lines: readonly LogLine[]) => lines.map((line) => line.raw)

describe('filterConsoleLines 级别档位', () => {
  const lines = build([
    [
      [
        '[09:00:01] [Server thread/INFO]: plain info',
        '[09:00:02] [Server thread/WARN]: can not keep up',
        '[09:00:03] [Server thread/ERROR]: boom',
        '[09:00:04] [Server thread/DEBUG]: noisy',
        '',
      ].join('\n'),
      'stdout',
    ],
  ])

  it('all 原样返回同一引用（不做无谓拷贝）', () => {
    expect(filterConsoleLines(lines, 'all')).toBe(lines)
  })

  it('warn 档保留 WARN 与 ERROR，滤掉 INFO/DEBUG', () => {
    expect(raws(filterConsoleLines(lines, 'warn'))).toEqual([
      '[09:00:02] [Server thread/WARN]: can not keep up',
      '[09:00:03] [Server thread/ERROR]: boom',
    ])
  })

  it('error 档只保留 ERROR', () => {
    expect(raws(filterConsoleLines(lines, 'error'))).toEqual(['[09:00:03] [Server thread/ERROR]: boom'])
  })

  it('DEBUG 与 TRACE 同档，都低于 INFO（warn 档下都被滤掉）', () => {
    const noisy = build([['[09:00:01] [Server thread/TRACE]: t\n[09:00:02] [Server thread/DEBUG]: d\n', 'stdout']])
    expect(filterConsoleLines(noisy, 'warn')).toEqual([])
  })

  it('stderr 无级别前缀的行按 ERROR 兜底，故 error 档仍可见（与后端 log_ingest 同一套推断）', () => {
    const lines = build([['naked stderr line\n', 'stderr']])
    expect(raws(filterConsoleLines(lines, 'error'))).toEqual(['naked stderr line'])
  })
})

describe('filterConsoleLines 堆栈块整块保留（spec §3.2，为 FR-418 折叠留接缝）', () => {
  // 典型 Paper 现场：ERROR 抬头 → 异常头 → 帧 → Caused by → 帧 → ... N more
  const lines = build([
    [
      [
        '[09:00:01] [Server thread/INFO]: before',
        '[09:00:02] [Server thread/ERROR]: Could not pass event PlayerJoinEvent',
        'java.lang.NullPointerException: boom',
        '\tat com.foo.Bar.baz(Bar.java:12)',
        '\tat com.foo.Qux.run(Qux.java:34)',
        'Caused by: java.lang.IllegalStateException: inner',
        '\tat com.foo.Deep.dig(Deep.java:56)',
        '\t... 23 more',
        '[09:00:03] [Server thread/INFO]: after',
        '',
      ].join('\n'),
      'stdout',
    ],
  ])

  it('切 ERROR 后堆栈块整块保留，不会只剩异常头', () => {
    const kept = filterConsoleLines(lines, 'error')
    const keptRaws = raws(kept)
    // 帧与 Caused by 链全在——它们的级别是 INFO 兜底，若单独按级别判定会被全滤掉。
    expect(keptRaws).toContain('\tat com.foo.Bar.baz(Bar.java:12)')
    expect(keptRaws).toContain('\tat com.foo.Qux.run(Qux.java:34)')
    expect(keptRaws).toContain('Caused by: java.lang.IllegalStateException: inner')
    expect(keptRaws).toContain('\tat com.foo.Deep.dig(Deep.java:56)')
    expect(keptRaws).toContain('\t... 23 more')
    // 无关的 INFO 行被滤掉，证明过滤真的生效了（而不是「什么都没滤」的假通过）。
    expect(keptRaws).not.toContain('[09:00:01] [Server thread/INFO]: before')
    expect(keptRaws).not.toContain('[09:00:03] [Server thread/INFO]: after')
  })

  it('堆栈块相对异常头的顺序不变（块是连续的一段）', () => {
    const kept = raws(filterConsoleLines(lines, 'error'))
    const headAt = kept.indexOf('[09:00:02] [Server thread/ERROR]: Could not pass event PlayerJoinEvent')
    expect(headAt).toBeGreaterThanOrEqual(0)
    // 头之后紧跟异常头行与全部帧，中间不插入别的东西。
    expect(kept.slice(headAt + 1)).toEqual([
      'java.lang.NullPointerException: boom',
      '\tat com.foo.Bar.baz(Bar.java:12)',
      '\tat com.foo.Qux.run(Qux.java:34)',
      'Caused by: java.lang.IllegalStateException: inner',
      '\tat com.foo.Deep.dig(Deep.java:56)',
      '\t... 23 more',
    ])
  })

  it('异常头被滤掉时，其帧一并隐去（帧不会孤零零地漂在屏上）', () => {
    // 一个纯 INFO 抬头的堆栈：warn 档下整块都不该出现。
    const infoStack = build([
      [
        [
          '[09:00:02] [Server thread/INFO]: harmless notice',
          'java.lang.RuntimeException: soft',
          '\tat com.foo.A.b(A.java:1)',
          '[09:00:03] [Server thread/WARN]: real warn',
          '',
        ].join('\n'),
        'stdout',
      ],
    ])
    expect(raws(filterConsoleLines(infoStack, 'warn'))).toEqual(['[09:00:03] [Server thread/WARN]: real warn'])
  })

  it('孤儿堆栈行（缓冲把头丢了）退化为按自身级别判定，不静默吞掉异常现场', () => {
    // 帧作为第一行出现 → 无上一行可作头，stackOf 为空；stderr 兜底 ERROR，故 error 档保留。
    const orphan = build([['\tat com.foo.Orphan.x(Orphan.java:1)\n', 'stderr']])
    expect(orphan[0].stackOf).toBeUndefined()
    expect(raws(filterConsoleLines(orphan, 'error'))).toEqual(['\tat com.foo.Orphan.x(Orphan.java:1)'])
  })
})

describe('filterConsoleLines 上下文行（command / system）', () => {
  function withContext() {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('[09:00:01] [Server thread/INFO]: info\n', 'stdout')
    buffer.appendCommand('reload confirm')
    buffer.appendSystem('[连接已断开]')
    buffer.appendChunk('[09:00:02] [Server thread/ERROR]: boom\n', 'stdout')
    return buffer.snapshot()
  }

  it('展示用过滤保留命令回显与系统提示：没有「我刚 reload 过」就读不出因果', () => {
    const kept = raws(filterConsoleLines(withContext(), 'error'))
    expect(kept).toEqual(['reload confirm', '[连接已断开]', '[09:00:02] [Server thread/ERROR]: boom'])
  })

  it('keepContext:false 时只剩错误本身（供「贴给别人看」的纯错误文本）', () => {
    const kept = raws(filterConsoleLines(withContext(), 'error', { keepContext: false }))
    expect(kept).toEqual(['[09:00:02] [Server thread/ERROR]: boom'])
  })

  it('all 档 + keepContext:false 也会剥掉上下文（不是所有档位都短路返回原数组）', () => {
    const kept = raws(filterConsoleLines(withContext(), 'all', { keepContext: false }))
    expect(kept).not.toContain('reload confirm')
    expect(kept).not.toContain('[连接已断开]')
    expect(kept).toContain('[09:00:01] [Server thread/INFO]: info')
  })
})

describe('复制取材', () => {
  it('consoleLinesToText 用 raw 而非重排后的列（贴出去要对得上日志检索）', () => {
    const lines = build([['[09:00:01] [Server thread/WARN]: [MyPlugin] hi\n', 'stdout']])
    // body 已被拆掉时间/级别/来源前缀，raw 是完整原文。
    expect(lines[0].body).toBe('hi')
    expect(consoleLinesToText(lines)).toBe('[09:00:01] [Server thread/WARN]: [MyPlugin] hi')
  })

  it('多行以 \\n 连接，空数组给空串', () => {
    const lines = build([['a\nb\n', 'stdout']])
    expect(consoleLinesToText(lines)).toBe('a\nb')
    expect(consoleLinesToText([])).toBe('')
  })

  it('consoleErrorLines = ERROR + 完整堆栈，且不含命令回显/系统提示', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendCommand('reload')
    buffer.appendChunk('[09:00:02] [Server thread/ERROR]: boom\n\tat com.foo.A.b(A.java:1)\n', 'stdout')
    buffer.appendChunk('[09:00:03] [Server thread/INFO]: fine\n', 'stdout')
    expect(raws(consoleErrorLines(buffer.snapshot()))).toEqual([
      '[09:00:02] [Server thread/ERROR]: boom',
      '\tat com.foo.A.b(A.java:1)',
    ])
  })
})
