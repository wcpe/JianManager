/**
 * 状态上报器。
 * 周期收集全部 Bot runtime snapshot 并通过 bot-state 上报。
 */

import { sendEvent } from '../index.js'
import type { BotStateSnapshot } from '../ipc/types.js'

export type { BotStateSnapshot } from '../ipc/types.js'

/** 状态上报配置。 */
export interface StateReporterConfig {
  intervalMs: number
}

/** 状态上报器。 */
export class StateReporter {
  private intervalId: ReturnType<typeof setInterval> | null = null
  private readonly intervalMs: number
  private snapshotProvider: (() => BotStateSnapshot[]) | null = null

  constructor(config: StateReporterConfig) {
    this.intervalMs = config.intervalMs
  }

  /** 设置状态快照提供函数。 */
  setSnapshotProvider(provider: () => BotStateSnapshot[]): void {
    this.snapshotProvider = provider
  }

  /** 启动周期性上报。 */
  start(): void {
    if (this.intervalId) return
    this.intervalId = setInterval(() => this.report(), this.intervalMs)
  }

  /** 停止上报。 */
  stop(): void {
    if (this.intervalId) {
      clearInterval(this.intervalId)
      this.intervalId = null
    }
  }

  /** 立即上报一次状态。 */
  report(): void {
    const snapshots = this.snapshotProvider?.() ?? []
    if (snapshots.length === 0) return
    sendEvent({ evt: 'bot-state', bots: snapshots })
  }
}
