/**
 * 行为引擎基类。
 * 所有行为模式继承此类，实现 tick 方法。
 */

import type { Bot } from 'mineflayer'

export abstract class Behavior {
  protected botId: string
  protected running = false
  protected mcBot: Bot | null = null

  constructor(botId: string) {
    this.botId = botId
  }

  /** 绑定 Mineflayer Bot 实例。 */
  setMcBot(bot: Bot): void {
    this.mcBot = bot
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
    if (!this.running || !this.mcBot) return

    const player = this.mcBot.players[this.target]
    if (!player || !player.entity) return

    const pos = player.entity.position
    this.mcBot.lookAt(pos)

    // 简单跟随：如果玩家距离超过 3 格则走向玩家
    const dist = this.mcBot.entity.position.distanceTo(pos)
    if (dist > 3) {
      this.mcBot.setControlState('forward', true)
    } else {
      this.mcBot.setControlState('forward', false)
    }
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
    if (!this.running || !this.mcBot || this.waypoints.length === 0) return

    const target = this.waypoints[this.currentIndex]
    const currentPos = this.mcBot.entity.position
    const dx = target.x - currentPos.x
    const dy = target.y - currentPos.y
    const dz = target.z - currentPos.z
    const dist = Math.sqrt(dx * dx + dy * dy + dz * dz)

    if (dist < 2) {
      this.currentIndex = (this.currentIndex + 1) % this.waypoints.length
    } else {
      // 简单移动：设置前进状态
      this.mcBot.setControlState('forward', true)
      this.mcBot.setControlState('sprint', dist > 10)
    }
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
    if (!this.running || !this.mcBot) return

    // 检测附近敌对实体
    const hostile = this.mcBot.nearestEntity((entity) => {
      return entity.kind === 'Hostile mobs'
    })

    if (hostile) {
      this.mcBot.attack(hostile)
    }

    // 如果远离守卫位置则返回
    if (this.guardPos) {
      const currentPos = this.mcBot.entity.position
      const dx = this.guardPos.x - currentPos.x
      const dy = this.guardPos.y - currentPos.y
      const dz = this.guardPos.z - currentPos.z
      const dist = Math.sqrt(dx * dx + dy * dy + dz * dz)

      if (dist > 10) {
        this.mcBot.setControlState('forward', true)
      }
    }
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
