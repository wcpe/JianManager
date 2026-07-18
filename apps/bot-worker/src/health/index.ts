/**
 * 心跳检测模块。
 * 定期上报进程存活、Fleet 利用率和事件循环延迟。
 */

import { monitorEventLoopDelay } from 'node:perf_hooks'
import { sendEvent } from '../index.js'

/** 心跳配置。 */
export interface HealthConfig {
  intervalMs: number
  fleetHeartbeat?: (eventLoopP95Ms: number) => object
}

/** 心跳检测器。 */
export class HealthCheck {
  private intervalId: ReturnType<typeof setInterval> | null = null
  private readonly intervalMs: number
  private readonly fleetHeartbeat?: HealthConfig['fleetHeartbeat']
  private readonly eventLoopDelay = monitorEventLoopDelay({ resolution: 20 })
  private heartbeatCount = 0

  constructor(config: HealthConfig) {
    this.intervalMs = config.intervalMs
    this.fleetHeartbeat = config.fleetHeartbeat
  }

  /** 启动心跳与事件循环延迟采样。 */
  start(): void {
    if (this.intervalId) return
    this.eventLoopDelay.enable()
    this.intervalId = setInterval(() => this.report(), this.intervalMs)
  }

  /** 停止心跳与采样。 */
  stop(): void {
    if (this.intervalId) {
      clearInterval(this.intervalId)
      this.intervalId = null
    }
    this.eventLoopDelay.disable()
  }

  /** 获取心跳次数。 */
  count(): number {
    return this.heartbeatCount
  }

  private report(): void {
    this.heartbeatCount++
    const eventLoopP95Ms = this.eventLoopDelay.percentile(95) / 1_000_000
    this.eventLoopDelay.reset()
    sendEvent({
      evt: 'heartbeat',
      seq: this.heartbeatCount,
      uptime: process.uptime(),
      pid: process.pid,
      memory: process.memoryUsage().rss,
      ...(this.fleetHeartbeat?.(eventLoopP95Ms) ?? {}),
    })
  }
}
