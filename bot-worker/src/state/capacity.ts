/**
 * 容量限制器。
 * 限制单个 Worker 上的最大 Bot 数量（默认 50）。
 */

/** 容量限制配置。 */
export interface CapacityConfig {
  /** 最大 Bot 数量。 */
  maxBots: number
}

/** 容量限制器。 */
export class CapacityLimiter {
  private maxBots: number
  private currentCount = 0

  constructor(config: CapacityConfig) {
    this.maxBots = config.maxBots
  }

  /** 检查是否还能创建更多 Bot。 */
  canCreate(count: number): boolean {
    return this.currentCount + count <= this.maxBots
  }

  /** 增加计数。 */
  add(count: number): void {
    this.currentCount += count
  }

  /** 减少计数。 */
  remove(count: number): void {
    this.currentCount = Math.max(0, this.currentCount - count)
  }

  /** 获取当前数量。 */
  current(): number {
    return this.currentCount
  }

  /** 获取最大容量。 */
  max(): number {
    return this.maxBots
  }

  /** 获取剩余容量。 */
  remaining(): number {
    return Math.max(0, this.maxBots - this.currentCount)
  }
}
