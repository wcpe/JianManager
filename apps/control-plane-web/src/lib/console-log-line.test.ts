import { describe, expect, it } from 'vitest'

import { createLogLineParser, type LogLine, type LogStream } from './console-log-line'

const ESC = '\u001b'

/** 逐行喂进同一个解析器（跨行状态如 stackOf 需要顺序），返回全部行。 */
function parseAll(raws: string[], stream: LogStream = 'stdout'): LogLine[] {
  const parser = createLogLineParser()
  return raws.map((raw, index) => parser.parse(raw, { seq: index, stream }))
}

function parseOne(raw: string, stream: LogStream = 'stdout'): LogLine {
  return parseAll([raw], stream)[0]
}

/**
 * 日志行解析（FR-415，spec §1.2）。
 *
 * 判据是**保守**：认得的格式要拆干净，认不出的一律降级为纯文本且字段留空——
 * 猜错的时间戳/级别会直接把 FR-417 的过滤与 FR-419 的定位带偏。
 */
describe('LogLineParser 前缀解析', () => {
  it('MC 原生 / Paper 格式：拆出时间与级别，线程名不入模型但留在 raw', () => {
    const line = parseOne('[09:12:13] [Server thread/INFO]: Done (12.345s)!')
    expect(line.ts).toBe('09:12:13')
    expect(line.level).toBe('INFO')
    expect(line.body).toBe('Done (12.345s)!')
    expect(line.raw).toBe('[09:12:13] [Server thread/INFO]: Done (12.345s)!')
    expect(line.kind).toBe('log')
    expect(line.source).toBeUndefined()
  })

  it('精简格式 [HH:MM:SS LEVEL]: 同样拆出时间与级别', () => {
    const line = parseOne('[09:12:13 WARN]: Can\'t keep up!')
    expect(line.ts).toBe('09:12:13')
    expect(line.level).toBe('WARN')
    expect(line.body).toBe("Can't keep up!")
  })

  it('级别别名归一：WARNING→WARN、SEVERE/FATAL→ERROR', () => {
    expect(parseOne('[09:12:13] [main/WARNING]: x').level).toBe('WARN')
    expect(parseOne('[09:12:13] [main/SEVERE]: x').level).toBe('ERROR')
    expect(parseOne('[09:12:13] [main/FATAL]: x').level).toBe('ERROR')
    expect(parseOne('[09:12:13] [main/DEBUG]: x').level).toBe('DEBUG')
    expect(parseOne('[09:12:13] [main/TRACE]: x').level).toBe('TRACE')
  })

  it('带线程名里含斜杠 / 数字 / 空格的前缀仍能拆对', () => {
    const line = parseOne('[09:12:13] [Craft Scheduler Thread - 3/ERROR]: boom')
    expect(line.ts).toBe('09:12:13')
    expect(line.level).toBe('ERROR')
    expect(line.body).toBe('boom')
  })

  it('正文开头的 [Xxx] 取作来源', () => {
    const line = parseOne('[09:12:13] [Server thread/INFO]: [WorldEdit] Loaded 5 regions')
    expect(line.source).toBe('WorldEdit')
    expect(line.body).toBe('Loaded 5 regions')
  })

  it('Paper 给前缀套色时仍能拆前缀，且正文保留前缀里的 SGR（不失色）', () => {
    const raw = `${ESC}[0;32m[09:12:13] [Server thread/INFO]:${ESC}[0;39m Hello`
    const line = parseOne(raw)
    expect(line.ts).toBe('09:12:13')
    expect(line.level).toBe('INFO')
    // 前缀里的两条 SGR 前置保留，正文文字本身是 Hello。
    expect(line.body).toBe(`${ESC}[0;32m${ESC}[0;39mHello`)
    expect(line.raw).toBe(raw)
  })
  it('无时间前缀的续行继承上一行的 ts（Paper 多行告警不穿越时间流）', () => {
    const [first, second] = parseAll([
      '[06:30:10] [Server thread/WARN]: a',
      'We recommend installing the spark profiler...',
    ])
    expect(first.ts).toBe('06:30:10')
    // 续行没有 [HH:MM:SS] 前缀，继承头行的时刻，而不是被打上「行到达时刻」造成时间穿越。
    expect(second.ts).toBe('06:30:10')
    // tsParsed 只在本行自身命中时间前缀时置位（FR-417 级别过滤据此区分记录头与续行，
    // 评审 P0-2：只看 ts 有无会把继承到时间的续行误当成新记录，滤丢整块堆栈）。
    expect(first.tsParsed).toBe(true)
    expect(second.tsParsed).toBeUndefined()
  })
})

describe('LogLineParser 解析不出就降级纯文本（绝不猜）', () => {
  const degraded: Array<[string, string]> = [
    ['无任何前缀', 'plain server output'],
    ['只有时间没级别', '[09:12:13] something happened'],
    ['级别 token 不在白名单', '[09:12:13] [Server thread/NOTICE]: x'],
    ['时间不是 HH:MM:SS', '[9:2:3] [Server thread/INFO]: x'],
    ['方括号未闭合', '[09:12:13 [Server thread/INFO]: x'],
    ['缺冒号', '[09:12:13] [Server thread/INFO] x'],
    ['日期时间格式（非本 spec 覆盖）', '2026-08-27 09:12:13 INFO x'],
    ['空行', ''],
  ]

  it.each(degraded)('%s → kind=log，ts/source 留空，body=原文', (_name, raw) => {
    const line = parseOne(raw)
    expect(line.kind).toBe('log')
    expect(line.ts).toBeUndefined()
    expect(line.source).toBeUndefined()
    expect(line.body).toBe(raw)
    expect(line.raw).toBe(raw)
  })

  it('纯时间样式的 token 不得被误当作来源', () => {
    // `[09:12:13] xxx` 时间前缀不匹配（无级别），此时 source 规则若不设防会取到 09:12:13。
    expect(parseOne('[09:12:13] something happened').source).toBeUndefined()
  })

  it('带空格的 [Server thread/INFO] 不得被当作来源', () => {
    expect(parseOne('[Server thread/INFO] x').source).toBeUndefined()
  })
})

describe('LogLineParser 级别兜底（spec §1.2：与后端 log_ingest 同一套推断）', () => {
  it('stdout 无级别兜底 INFO，stderr 无级别兜底 ERROR', () => {
    expect(parseOne('plain', 'stdout').level).toBe('INFO')
    expect(parseOne('plain', 'stderr').level).toBe('ERROR')
  })

  it('已解析出级别时不被流兜底覆盖', () => {
    expect(parseOne('[09:12:13] [main/WARN]: x', 'stderr').level).toBe('WARN')
  })

  it('stderr 上的 Go slog level=INFO 不得按 ERROR 兜底（Beacon 等全量 stderr 进程）', () => {
    const raw =
      'time=2026-09-20T12:24:28.732+08:00 level=INFO msg=访问 方法=GET 路径=/beacon/v2/agent/registration 状态=200'
    const line = parseOne(raw, 'stderr')
    expect(line.level).toBe('INFO')
  })

  it('结构化 level= 覆盖各常见级别与引号形态，且不被 stream 兜底改写', () => {
    expect(parseOne('time=x level=error msg=失败', 'stdout').level).toBe('ERROR')
    expect(parseOne('time=x level=WARN msg=慢', 'stderr').level).toBe('WARN')
    expect(parseOne('time=x level="debug" msg=细节', 'stderr').level).toBe('DEBUG')
  })

  it('正文里非 level 键值（如 状态=200）不得污染级别；无 level= 时仍按 stream 兜底', () => {
    expect(parseOne('状态=200 耗时=1ms', 'stderr').level).toBe('ERROR')
    expect(parseOne('msg=访问 level_token=INFO', 'stderr').level).toBe('ERROR')
  })
})

describe('LogLineParser 堆栈（为 FR-418 铺路）', () => {
  it('异常头 + 帧 + Caused by：帧与 cause 全部 stackOf 指向头行 seq', () => {
    const lines = parseAll([
      '[09:12:13] [Server thread/ERROR]: java.lang.NullPointerException: boom',
      '\tat com.example.Plugin.onEnable(Plugin.java:42)',
      '\tat org.bukkit.Server.run(Server.java:1)',
      'Caused by: java.lang.IllegalStateException: inner',
      '\tat com.example.Deep.call(Deep.java:7)',
      '\t... 23 more',
    ])

    expect(lines[0].kind).toBe('log')
    expect(lines.slice(1).map((l) => l.kind)).toEqual([
      'stack-frame',
      'stack-frame',
      'stack-cause',
      'stack-frame',
      'stack-frame',
    ])
    expect(lines.slice(1).map((l) => l.stackOf)).toEqual([0, 0, 0, 0, 0])
  })

  it('头行不含 Exception 字样时，按「其后紧跟 stack-frame」认作异常头（spec §1.2 规则 5）', () => {
    const lines = parseAll([
      '[09:12:13] [Server thread/ERROR]: Could not pass event PlayerJoinEvent to MyPlugin',
      '\tat com.example.Plugin.on(Plugin.java:9)',
    ])
    expect(lines[1].stackOf).toBe(0)
  })

  it('普通行终结堆栈块：后续帧挂到新的上一行，不再挂到旧异常头', () => {
    const lines = parseAll([
      'java.lang.NullPointerException: first',
      '\tat a.B.c(B.java:1)',
      '[09:12:13] [Server thread/INFO]: unrelated line',
      '\tat x.Y.z(Y.java:2)',
    ])
    expect(lines[1].stackOf).toBe(0)
    // 第 3 行是普通行、把块打断；第 4 行的头因此是第 3 行，而不是 seq 0。
    expect(lines[3].stackOf).toBe(2)
  })

  it('缓冲开头就是堆栈帧（更早的头已被丢弃）时 stackOf 留空，不乱指', () => {
    expect(parseOne('\tat a.B.c(B.java:1)').stackOf).toBeUndefined()
  })

  it('正文里提到 Exception 这个词不算异常头', () => {
    const lines = parseAll([
      '[09:12:13] [Server thread/INFO]: caught an Exception while saving',
      'plain follow-up',
      '\tat a.B.c(B.java:1)',
    ])
    // 第 3 行的头应是紧邻的第 2 行，而不是含 Exception 字样的第 1 行。
    expect(lines[2].stackOf).toBe(1)
  })

  it('带时间前缀的堆栈帧也识别为 stack-frame', () => {
    const lines = parseAll([
      '[09:12:13] [Server thread/ERROR]: java.lang.RuntimeException: x',
      '[09:12:13] [Server thread/ERROR]: \tat a.B.c(B.java:1)',
    ])
    expect(lines[1].kind).toBe('stack-frame')
    expect(lines[1].stackOf).toBe(0)
  })
})

describe('LogLineParser 合成行', () => {
  it('命令回显与系统提示 kind 正确、body=raw、无级别', () => {
    const parser = createLogLineParser()
    const command = parser.synthesize('say hello', 0, 'command')
    expect(command).toEqual({ seq: 0, body: 'say hello', raw: 'say hello', kind: 'command' })
    const system = parser.synthesize('[连接已断开]', 1, 'system')
    expect(system.kind).toBe('system')
    expect(system.level).toBeUndefined()
  })

  it('合成行打断堆栈上下文：其后的帧挂到合成行而非旧异常头', () => {
    const parser = createLogLineParser()
    parser.parse('java.lang.NullPointerException: x', { seq: 0, stream: 'stdout' })
    parser.synthesize('say hi', 1, 'command')
    const frame = parser.parse('\tat a.B.c(B.java:1)', { seq: 2, stream: 'stdout' })
    expect(frame.stackOf).toBe(1)
  })
})

describe('LogLineParser.preview 不推进状态', () => {
  it('同一未落定尾段多次 preview 后再 parse，堆栈归属与只 parse 一次相同', () => {
    const parser = createLogLineParser()
    parser.parse('java.lang.NullPointerException: x', { seq: 0, stream: 'stdout' })
    // 尾段先被预览两次（虚拟列表每次渲染都会取快照）。
    parser.preview('plain tail', { seq: 1, stream: 'stdout' })
    parser.preview('plain tail', { seq: 1, stream: 'stdout' })
    const settled = parser.parse('plain tail', { seq: 1, stream: 'stdout' })
    expect(settled.kind).toBe('log')
    const frame = parser.parse('\tat a.B.c(B.java:1)', { seq: 2, stream: 'stdout' })
    // preview 若污染了状态，这里会挂到 seq 0 而不是被打断后的 seq 1。
    expect(frame.stackOf).toBe(1)
  })
})
