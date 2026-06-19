/**
 * 行为引擎基类。
 * 独立成文件以打破 index.ts ↔ custom.ts 的循环依赖：
 * custom.ts `extends Behavior`，而 index.ts 又导入 CustomBehavior 用于工厂，
 * 二者互相 import 会导致 ESM 加载期 `Behavior` 处于 TDZ 而崩溃。
 */

import type { Bot } from 'mineflayer'
import { PathfinderMover } from '../pathfinder/index.js'

/** 所有行为模式的基类，子类实现 tick 与 name。 */
export abstract class Behavior {
  protected botId: string
  protected running = false
  protected mcBot: Bot | null = null
  /** 寻路移动器，首次 tick 时惰性初始化。 */
  protected mover: PathfinderMover | null = null
  private moverInitialized = false

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
    if (this.mover) {
      this.mover.stop()
    }
  }

  /** 每 250ms 调用一次。 */
  abstract tick(): Promise<void>

  /** 行为名称。 */
  abstract get name(): string

  /** 确保 pathfinder 已初始化（惰性加载）。 */
  protected async ensureMover(): Promise<void> {
    if (this.moverInitialized || !this.mcBot) return
    this.mover = new PathfinderMover(this.mcBot)
    await this.mover.init()
    this.moverInitialized = true
  }

  /** 寻路是否就绪。 */
  protected isPathfinderReady(): boolean {
    return this.moverInitialized && this.mover !== null && this.mover.isReady()
  }
}
