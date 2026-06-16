/**
 * 预热池。
 * 启动时预创建 N 个空闲 Bot 连接，按需取用，减少首次创建 Bot 的延迟。
 *
 * 管理逻辑：
 *   - 启动时按配置创建 prewarmCount 个空闲 bot
 *   - 空闲 bot 未连接到 MC 服务器，仅占位
 *   - 创建 Bot 请求时优先从池中取用
 *   - 池空时走正常创建流程
 *   - 停止 Bot 时将其归还池中（如池未满）
 */

import type { Bot } from 'mineflayer'
import { sendEvent } from '../index.js'

/** 预热池中的空闲 Bot 条目。 */
interface PrewarmEntry {
  /** 池中 bot 的内部 ID（非 MC bot ID）。 */
  poolId: string
  /** 是否已被取用。 */
  claimed: boolean
}

/** 预热池配置。 */
export interface PrewarmConfig {
  /** 预热数量。 */
  count: number
  /** 最大池容量。 */
  maxPoolSize: number
}

/** 预热池。 */
export class PrewarmPool {
  private pool: PrewarmEntry[] = []
  private maxPoolSize: number

  constructor(config: PrewarmConfig) {
    this.maxPoolSize = config.maxPoolSize
  }

  /** 初始化预热池，创建指定数量的空闲占位。 */
  init(count: number): void {
    for (let i = 0; i < count; i++) {
      this.pool.push({
        poolId: `prewarm_${Date.now()}_${i}`,
        claimed: false,
      })
    }
    sendEvent({
      evt: 'bot-event',
      type: 'prewarm-init',
      data: { count: this.pool.length },
    })
  }

  /** 从池中取用一个预热条目。返回 poolId，池空返回 null。 */
  claim(): string | null {
    const entry = this.pool.find((e) => !e.claimed)
    if (!entry) return null
    entry.claimed = true
    return entry.poolId
  }

  /** 归还一个预热条目到池中。池满时不归还。 */
  release(poolId: string): boolean {
    if (this.pool.length >= this.maxPoolSize) return false

    const idx = this.pool.findIndex((e) => e.poolId === poolId && e.claimed)
    if (idx !== -1) {
      this.pool[idx].claimed = false
      return true
    }
    return false
  }

  /** 添加新的预热条目到池中。 */
  add(): boolean {
    if (this.pool.length >= this.maxPoolSize) return false
    this.pool.push({
      poolId: `prewarm_${Date.now()}_${this.pool.length}`,
      claimed: false,
    })
    return true
  }

  /** 获取池状态。 */
  stats(): { total: number; available: number; claimed: number } {
    const available = this.pool.filter((e) => !e.claimed).length
    return {
      total: this.pool.length,
      available,
      claimed: this.pool.length - available,
    }
  }

  /** 清空预热池。 */
  clear(): void {
    this.pool = []
  }
}
