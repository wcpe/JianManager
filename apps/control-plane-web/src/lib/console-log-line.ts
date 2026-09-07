import { ansiPlainText, ansiSliceFromPlain } from './console-ansi'

/**
 * 控制台日志行模型与解析器（FR-415，spec §1.1/§1.2）。
 *
 * 输出区改成 DOM 虚拟列表后（ADR-086），行不再是「一串字节」而是结构化记录：
 * 时间 / 级别 / 来源拆成独立列，正文单列。级别过滤（FR-417）、堆栈折叠与锚点选区（FR-418）、
 * 历史回溯（FR-419）全部建立在这个模型上，所以三条 FR 共用本解析器。
 *
 * 解析**保守优先**：每一步的模式不匹配即什么都不填，降级为纯文本行（`kind:'log'`，字段留空）。
 * 宁可少解析出一个字段，也不猜——猜错的时间戳/级别会直接把过滤与定位带偏。
 */

export type LogKind = 'log' | 'command' | 'stack-frame' | 'stack-cause' | 'system'

export type LogLevel = 'INFO' | 'WARN' | 'ERROR' | 'DEBUG' | 'TRACE'

/** 行的来源流。级别兜底用（spec §1.2：stderr→ERROR、stdout→INFO）。 */
export type LogStream = 'stdout' | 'stderr'

/** 结构化日志行。`seq` 是环形缓冲内的唯一单调标识——选区与定位一律用它，不用数组下标。 */
export interface LogLine {
  seq: number
  /** 行首解析出的时间（形如 `09:12:13`）；无前缀的续行继承上一行的 ts（`lastTs`），全程无源则 undefined。 */
  ts?: string
  /**
   * `ts` 是否来自**本行自身**的时间前缀解析（即该行自带完整记录头）。
   * 经 `lastTs` 继承到时间的续行**不置位**：它们仍是上一条记录的续行，
   * 级别过滤（FR-417）据此区分「记录头」与「续行」——若只看 `ts` 有无，
   * 续行会被误当成新记录、按自身兜底级别参与过滤，把整块堆栈滤丢。
   */
  tsParsed?: boolean
  level?: LogLevel
  /** 正文开头的 `[Xxx]`（`[Server]` / 插件名等）。 */
  source?: string
  /** 去掉已解析前缀后的正文，**保留 ANSI 段**（渲染用）。 */
  body: string
  /** 原始整行。复制与导出一律用它——用户要的是原始日志，不是我们重排过的（spec §1.1）。 */
  raw: string
  kind: LogKind
  /** stack-frame / stack-cause 指向其异常头行的 seq；找不到头则不填。 */
  stackOf?: number
}

/**
 * 级别 token 的归一化表。
 *
 * 只收录明确等价的别名（Log4j 的 `WARNING`、JUL 的 `SEVERE`、Log4j 的 `FATAL`）；
 * 表外的 token 视为「前缀不匹配」整体降级，而不是猜一个级别塞进去。
 */
const LEVEL_ALIASES: Record<string, LogLevel> = {
  INFO: 'INFO',
  WARN: 'WARN',
  WARNING: 'WARN',
  ERROR: 'ERROR',
  SEVERE: 'ERROR',
  FATAL: 'ERROR',
  DEBUG: 'DEBUG',
  TRACE: 'TRACE',
}

const LEVEL_TOKEN = '(INFO|WARNING|WARN|ERROR|SEVERE|FATAL|DEBUG|TRACE)'

/** MC 原生 / Paper：`[09:12:13] [Server thread/INFO]: 正文`。 */
const PREFIX_TIME_THREAD_LEVEL = new RegExp(`^\\[(\\d{2}:\\d{2}:\\d{2})\\] \\[([^\\]]*)/${LEVEL_TOKEN}\\]:[ ]?`)

/** 精简格式：`[09:12:13 INFO]: 正文`。 */
const PREFIX_TIME_LEVEL = new RegExp(`^\\[(\\d{2}:\\d{2}:\\d{2}) ${LEVEL_TOKEN}\\]:[ ]?`)

/** 正文开头的来源标签。禁空白与嵌套括号，故 `[Server thread/INFO]` 这类带空格的不会被误当来源。 */
const PREFIX_SOURCE = /^\[([^[\]\s]{1,32})\][ ]?/

/**
 * 只由数字与冒号组成的 token：`[12:34:56] 无级别` 这类行不得把时间误认成来源。
 *
 * 不写成严格的 `\d{2}:\d{2}:\d{2}`——`[9:2:3]` 这种非补零时间同样是时间不是来源，
 * 而真实的插件名不会只有数字和冒号。
 */
const DIGITS_AND_COLONS_ONLY = /^[\d:]+$/

/** Java 堆栈帧：`\tat <fqcn>(<file>:<line>)`。用 `\s` 而非字面制表符，避开控制字符正则。 */
const STACK_FRAME = /^\s+at\s+\S+\(.*\)\s*$/

/** 堆栈省略尾：`\t... 23 more`。 */
const STACK_MORE = /^\s+\.\.\.\s+\d+\s+more\s*$/

/** 异常链：`Caused by: java.lang.XxxException: …`。 */
const STACK_CAUSE = /^Caused by:\s+\S/

/**
 * 异常头：`(<module>/)?<fqcn>(Exception|Error|Throwable)` 后跟 `:` 或行尾。
 * 锚定行首而非全行搜索——「正文里提到 Exception 这个词」不该被认成异常头。
 */
const EXCEPTION_HEAD = /^(?:[\w.$]+\/)?(?:[A-Za-z_$][\w$]*\.)*[A-Za-z_$][\w$]*(?:Exception|Error|Throwable)(?::|$)/

/** 解析所需的可变上下文（`stackOf` 要跨行才能确定）。 */
interface ParserState {
  /** 当前堆栈块的异常头 seq。 */
  headSeq?: number
  /** 上一条非堆栈行的 seq——实现 spec §1.2 规则 5 的「其后紧跟 stack-frame 者即为异常头」。 */
  prevSeq?: number
  /**
   * 最近一次解析出的时间前缀：Paper 多行日志（如 spark 告警的续行）没有
   * `[HH:MM:SS]` 前缀，继承它才能让同一组输出共享同一时刻，而不是各自猜时间。
   */
  lastTs?: string
}

/** 解析一行的输入。 */
export interface ParseLineOptions {
  seq: number
  stream: LogStream
}

/** 前缀解析结果。 */
interface PrefixResult {
  ts?: string
  level?: LogLevel
  source?: string
  body: string
}

/** 拆时间 / 级别 / 来源前缀，返回剩余正文（保留 ANSI）。任一模式不匹配即留空该字段。 */
function parsePrefix(raw: string): PrefixResult {
  const { plain, map } = ansiPlainText(raw)

  let consumed = 0
  let ts: string | undefined
  let level: LogLevel | undefined

  const threadMatch = PREFIX_TIME_THREAD_LEVEL.exec(plain)
  const shortMatch = threadMatch ? null : PREFIX_TIME_LEVEL.exec(plain)
  if (threadMatch) {
    ts = threadMatch[1]
    // 线程名（threadMatch[2]）不入模型：spec §1.1 无该字段，需要时从 raw 取。
    level = LEVEL_ALIASES[threadMatch[3]]
    consumed = threadMatch[0].length
  } else if (shortMatch) {
    ts = shortMatch[1]
    level = LEVEL_ALIASES[shortMatch[2]]
    consumed = shortMatch[0].length
  }

  const afterTime = plain.slice(consumed)
  let source: string | undefined
  // 堆栈帧不取来源：`\tat com.foo.Bar(...)` 无 `[...]`，但避免 `[...]` 误伤仍显式跳过。
  if (!STACK_FRAME.test(afterTime) && !STACK_MORE.test(afterTime)) {
    const sourceMatch = PREFIX_SOURCE.exec(afterTime)
    if (sourceMatch && !DIGITS_AND_COLONS_ONLY.test(sourceMatch[1])) {
      source = sourceMatch[1]
      consumed += sourceMatch[0].length
    }
  }

  return { ts, level, source, body: ansiSliceFromPlain(raw, map, consumed) }
}

/** 按正文判定行类别（不含 command / system——那两类由合成入口直接指定）。 */
function classify(bodyPlain: string): Extract<LogKind, 'log' | 'stack-frame' | 'stack-cause'> {
  if (STACK_FRAME.test(bodyPlain) || STACK_MORE.test(bodyPlain)) return 'stack-frame'
  if (STACK_CAUSE.test(bodyPlain)) return 'stack-cause'
  return 'log'
}

/**
 * 日志行解析器。
 *
 * 有状态：`stackOf` 需要记住当前堆栈块的异常头，`preview` 因此必须提供**不改状态**的版本
 * ——行缓冲会把「尚未收到换行的尾段」当预览行渲染，若它污染状态，真正落定时的归属就错了。
 */
export class LogLineParser {
  private state: ParserState = {}

  /** 解析并推进状态（行已落定时调用）。 */
  parse(raw: string, options: ParseLineOptions): LogLine {
    const { line, state } = this.compute(raw, options, this.state)
    this.state = state
    return line
  }

  /** 解析但不推进状态（未落定的尾段预览）。 */
  preview(raw: string, options: ParseLineOptions): LogLine {
    return this.compute(raw, options, this.state).line
  }

  /**
   * 合成一条本地行（命令回显 / 系统提示）。
   *
   * 同时**打断堆栈上下文**：命令回显插在异常头与其帧之间时，后续帧不应再挂到那个头上。
   */
  synthesize(raw: string, seq: number, kind: Extract<LogKind, 'command' | 'system'>): LogLine {
    this.state = { prevSeq: seq }
    return { seq, body: raw, raw, kind }
  }

  /** 清空上下文（缓冲被清空时调用）。 */
  reset(): void {
    this.state = {}
  }

  private compute(
    raw: string,
    options: ParseLineOptions,
    state: ParserState,
  ): { line: LogLine; state: ParserState } {
    const { seq, stream } = options
    const prefix = parsePrefix(raw)
    const bodyPlain = ansiPlainText(prefix.body).plain
    const kind = classify(bodyPlain)

    // 级别兜底与后端 log_ingest 的推断保持同一套规则（stderr→ERROR、stdout→INFO），
    // 不引入第二套判定——否则同一行在控制台与日志中心会显示不同级别。
    const level = prefix.level ?? (stream === 'stderr' ? 'ERROR' : 'INFO')

    // 时间列一致性（FR-415）：ts 保持「解析不出就留空」的 spec 语义——**绝不猜**
    // （到达时刻会让 Paper 无前缀续行在时间流里「穿越」，正是要修的错位）；
    // 无前缀续行继承上一行的 ts（lastTs），使同一组多行输出共享同一时刻。
    // tsParsed 只在本行自身命中时间前缀时置位（继承不算），供 FR-417 区分记录头与续行。
    const ts = prefix.ts ?? state.lastTs

    const line: LogLine = {
      seq,
      ts,
      ...(prefix.ts !== undefined ? { tsParsed: true } : {}),
      level,
      source: prefix.source,
      body: prefix.body,
      raw,
      kind,
    }

    if (kind === 'log') {
      // 普通行终结上一个堆栈块；若它自身是异常头则成为新块的头。
      const isHead = EXCEPTION_HEAD.test(bodyPlain)
      return { line, state: { headSeq: isHead ? seq : undefined, prevSeq: seq, lastTs: ts } }
    }

    // 堆栈帧 / Caused by：优先挂到已识别的异常头；没有头时，按 spec §1.2 规则 5
    // 把「紧邻的上一行」认作异常头（如 `Could not pass event …` 这类不含 Exception 字样的头）。
    const headSeq = state.headSeq ?? state.prevSeq
    if (headSeq !== undefined) line.stackOf = headSeq
    line.kind = kind
    // prevSeq 不推进：堆栈帧自己不能当下一段帧的头。
    return { line, state: { headSeq, prevSeq: state.prevSeq, lastTs: ts ?? state.lastTs } }
  }
}

export function createLogLineParser(): LogLineParser {
  return new LogLineParser()
}
