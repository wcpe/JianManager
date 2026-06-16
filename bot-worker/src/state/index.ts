/**
 * 状态上报器。
 * 周期性（默认 3s）收集所有 Bot 状态并上报给 Worker Node。
 */

import { sendEvent } from '../index.js'

/** Bot 状态快照。 */
export interface BotStateSnapshot {
  id: string
  status: string
  name?: string
  health?: number
  food?: number
  position?: { x: number; y: number; z: number }
  dimension?: string
  behavior?: string
}

/** 状态上报配置。 */
export interface StateReporterConfig {
  /** 上报间隔毫秒。 */
  intervalMs: number
}

/** 状态上报器。 */
export class StateReporter {
  private intervalId: ReturnType<typeof setInterval> | null = null
  private intervalMs: number
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
    this.intervalId = setInterval(() => {
      this.report()
    }, this.intervalMs)
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
    if (!this.snapshotProvider) return
    const snapshots = this.snapshotProvider()
    if (snapshots.length === 0) return

    sendEvent({
      evt: 'bot-state',
      bots: snapshots,
    })
  }
}
