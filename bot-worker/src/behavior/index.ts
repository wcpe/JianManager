/**
 * 行为引擎基类。
 * 所有行为模式继承此类，实现 tick 方法。
 */

export abstract class Behavior {
  protected botId: string
  protected running = false

  constructor(botId: string) {
    this.botId = botId
  }

  /** 启动行为。 */
  start(): void {
    this.running = true
  }

  /** 停止行为。 */
  stop(): void {
    this.running = false
  }

  /** 每 250ms 调用一次。 */
  abstract tick(): Promise<void>

  /** 行为名称。 */
  abstract get name(): string
}

/**
 * 空闲行为：Bot 站立不动。
 */
export class IdleBehavior extends Behavior {
  get name() { return 'idle' }

  async tick() {
    // 什么都不做
  }
}

/**
 * 跟随行为：Bot 跟随指定玩家。
 */
export class FollowBehavior extends Behavior {
  private target: string

  constructor(botId: string, target: string) {
    super(botId)
    this.target = target
  }

  get name() { return 'follow' }

  async tick() {
    if (!this.running) return
    // TODO: 使用 mineflayer-pathfinder 跟随目标玩家
    // const player = this.bot.players[this.target]
    // if (player) { ... }
  }
}

/**
 * 巡逻行为：Bot 在指定区域内巡逻。
 */
export class PatrolBehavior extends Behavior {
  private waypoints: Array<{ x: number; y: number; z: number }> = []
  private currentIndex = 0

  constructor(botId: string) {
    super(botId)
  }

  get name() { return 'patrol' }

  setWaypoints(points: Array<{ x: number; y: number; z: number }>) {
    this.waypoints = points
    this.currentIndex = 0
  }

  async tick() {
    if (!this.running || this.waypoints.length === 0) return
    // TODO: 使用 mineflayer-pathfinder 移动到下一个路径点
    this.currentIndex = (this.currentIndex + 1) % this.waypoints.length
  }
}

/**
 * 守卫行为：Bot 在固定位置警戒，攻击敌对实体。
 */
export class GuardBehavior extends Behavior {
  private guardPos: { x: number; y: number; z: number } | null = null

  constructor(botId: string) {
    super(botId)
  }

  get name() { return 'guard' }

  setGuardPosition(pos: { x: number; y: number; z: number }) {
    this.guardPos = pos
  }

  async tick() {
    if (!this.running) return
    // TODO: 检测附近敌对实体并攻击
    // 如果远离守卫位置则返回
  }
}

/**
 * 创建行为实例。
 */
export function createBehavior(botId: string, type: string, target?: string): Behavior {
  switch (type) {
    case 'follow':
      return new FollowBehavior(botId, target || '')
    case 'patrol':
      return new PatrolBehavior(botId)
    case 'guard':
      return new GuardBehavior(botId)
    case 'idle':
    default:
      return new IdleBehavior(botId)
  }
}
