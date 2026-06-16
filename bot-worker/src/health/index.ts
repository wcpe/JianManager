/**
 * 心跳检测模块。
 * Bot Worker 进程定期向 Worker Node 发送心跳，用于存活检测。
 */

import { sendEvent } from '../index.js'

/** 心跳配置。 */
export interface HealthConfig {
  /** 心跳间隔毫秒。 */
  intervalMs: number
}

/** 心跳检测器。 */
export class HealthCheck {
  private intervalId: ReturnType<typeof setInterval> | null = null
  private intervalMs: number
  private heartbeatCount = 0

  constructor(config: HealthConfig) {
    this.intervalMs = config.intervalMs
  }

  /** 启动心跳。 */
  start(): void {
    if (this.intervalId) return
    this.intervalId = setInterval(() => {
      this.heartbeatCount++
      sendEvent({
        evt: 'heartbeat',
        seq: this.heartbeatCount,
        uptime: process.uptime(),
        pid: process.pid,
        memory: process.memoryUsage().rss,
      })
    }, this.intervalMs)
  }

  /** 停止心跳。 */
  stop(): void {
    if (this.intervalId) {
      clearInterval(this.intervalId)
      this.intervalId = null
    }
  }

  /** 获取心跳次数。 */
  count(): number {
    return this.heartbeatCount
  }
}
