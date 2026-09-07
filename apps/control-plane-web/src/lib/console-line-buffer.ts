import { createLogLineParser, type LogLine, type LogStream } from './console-log-line'

/**
 * 控制台行环形缓冲（FR-415，spec §2.1）。
 *
 * 取代 xterm 内部 buffer 成为「常驻缓冲」的载体（ADR-086）：会话保活语义不变——
 * 缓冲挂在 {@link import('./terminal-session-manager').TerminalSessionManager} 的会话上，
 * 渲染层卸载只解绑订阅，缓冲与 WS 都不动（ADR-067）。
 */

/** 缓冲上限（spec §2.1）。超出丢最旧，更早历史由 FR-419 从数据库游标分页取回。 */
export const CONSOLE_BUFFER_LIMIT = 5000

/** 按 `\r\n` / `\n` / 裸 `\r` 断行。裸 `\r` 断成多行而非原地覆盖，见 ADR-086 代价 5。 */
const LINE_BREAK = /\r\n|\n|\r/

export class ConsoleLineBuffer {
  private lines: LogLine[] = []
  private readonly parser = createLogLineParser()
  private nextSeq = 0
  private droppedLines = 0
  /** 会话创建时刻；未发生溢出时它就是历史回溯的接缝。`clear()` 不重置它。 */
  readonly startedAt = new Date()
  /** 每个已落定行的浏览器接收时刻，与 lines 始终同下标。仅用于历史回溯接缝，不参与展示。 */
  private receivedAt: number[] = []
  /** 尚未收到换行的尾段（WS 分包会把一行切两半）。 */
  private pendingText = ''
  private pendingStream: LogStream = 'stdout'
  /**
   * 快照缓存。`useSyncExternalStore` 要求 getSnapshot 在无变更时返回同一引用，
   * 否则会判定「快照未缓存」并陷入重渲染循环。
   */
  private snapshotCache: readonly LogLine[] | null = null

  /** 追加一段 stdout/stderr 原始文本，按行落定；未终止的尾段留待下次拼接。 */
  appendChunk(text: string, stream: LogStream): void {
    if (!text) return
    // 流切换时先把尾段落定：stdout 的半行与 stderr 的半行拼成一行会造出不存在的日志。
    if (this.pendingText && this.pendingStream !== stream) this.flushPending()
    this.pendingStream = stream

    const parts = (this.pendingText + text).split(LINE_BREAK)
    // 最后一段没有行尾终止符，仍是未完成的尾段。
    this.pendingText = parts.pop() ?? ''
    for (const part of parts) this.commit(this.parser.parse(part, { seq: this.nextSeq, stream }))
    this.invalidate()
  }

  /** 命令本地回显（spec §2.2：不等服务端，提交即入输出区）。 */
  appendCommand(line: string): LogLine {
    return this.appendSynthetic(line, 'command')
  }

  /** 系统提示行（连接断开 / 状态诊断等）。 */
  appendSystem(text: string): LogLine {
    return this.appendSynthetic(text, 'system')
  }

  /**
   * 当前全部行（含未落定的尾段预览）。
   *
   * 尾段用 `preview` 而非 `parse`：它稍后还会以同一个 `seq` 真正落定，
   * 提前推进解析器状态会让紧随其后的堆栈帧挂错异常头。
   */
  snapshot(): readonly LogLine[] {
    if (this.snapshotCache) return this.snapshotCache
    const out = this.pendingText
      ? [...this.lines, this.parser.preview(this.pendingText, { seq: this.nextSeq, stream: this.pendingStream })]
      : this.lines.slice()
    this.snapshotCache = out
    return out
  }

  /** 因超出上限被丢弃的行数（>0 时输出区顶部挂「更早日志需回溯」锚点）。 */
  get droppedCount(): number {
    return this.droppedLines
  }

  /** 已分配的 seq 总数（= 历史上出现过的行数，含已丢弃）。 */
  get totalSeq(): number {
    return this.nextSeq
  }

  /**
   * 历史回溯接缝（FR-419）。
   *
   * 未溢出时，内存缓冲覆盖整个会话，数据库只需取会话开始前的行；溢出后，`startedAt` 到
   * 当前首行之间正是被环形缓冲挤掉的日志。此时必须把接缝推进到**最早保留行的接收时刻**，
   * 否则向上滚会直接跨过这一段空洞。收到时刻和 DB 写入时刻可能相差极短，展示层会在边界
   * 以连续原文重叠去重（见 mergeHistoryAndLive），宁可保守保留一行也不漏整段。
   */
  get historyAnchorAt(): Date {
    if (this.droppedLines === 0 || this.receivedAt.length === 0) return this.startedAt
    return new Date(this.receivedAt[0])
  }

  /** 清空缓冲（「清屏」）。seq 不回绕——已被复制/引用的 seq 不能指向别的行。 */
  clear(): void {
    this.lines = []
    this.receivedAt = []
    this.pendingText = ''
    this.droppedLines = 0
    this.parser.reset()
    this.invalidate()
  }

  private appendSynthetic(text: string, kind: 'command' | 'system'): LogLine {
    // 先落定尾段：合成行插到半行中间会让那半行的 seq 排到合成行之后，顺序就乱了。
    this.flushPending()
    const line = this.parser.synthesize(text, this.nextSeq, kind)
    this.commit(line)
    this.invalidate()
    return line
  }

  private flushPending(): void {
    if (!this.pendingText) return
    const raw = this.pendingText
    this.pendingText = ''
    this.commit(this.parser.parse(raw, { seq: this.nextSeq, stream: this.pendingStream }))
  }

  private commit(line: LogLine): void {
    this.nextSeq++
    this.lines.push(line)
    this.receivedAt.push(Date.now())
    while (this.lines.length > CONSOLE_BUFFER_LIMIT) {
      this.lines.shift()
      this.receivedAt.shift()
      this.droppedLines++
    }
  }

  private invalidate(): void {
    this.snapshotCache = null
  }
}
