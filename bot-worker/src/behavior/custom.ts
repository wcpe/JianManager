/**
 * Custom 行为模式。
 * 接受 JSON 配置定义行为序列，Bot 按顺序执行每个步骤。
 *
 * 步骤类型：
 *   - move:    移动到指定坐标
 *   - chat:    发送聊天消息
 *   - wait:    等待指定毫秒
 *   - attack:  攻击最近的敌对实体
 *   - interact: 与指定方块交互
 *   - use_item: 使用手持物品
 */

import { Behavior } from './index.js'
import { PathfinderMover } from '../pathfinder/index.js'
import { sendEvent } from '../index.js'

/** 行为步骤定义。 */
export interface BehaviorStep {
  /** 步骤类型。 */
  type: 'move' | 'chat' | 'wait' | 'attack' | 'interact' | 'use_item'
  /** 目标坐标（move/interact）。 */
  pos?: { x: number; y: number; z: number }
  /** 聊天内容（chat）。 */
  message?: string
  /** 等待时间毫秒（wait）。 */
  duration?: number
  /** 是否循环整个序列。 */
  loop?: boolean
}

/** Custom 行为配置。 */
export interface CustomBehaviorConfig {
  /** 步骤序列。 */
  steps: BehaviorStep[]
  /** 是否循环执行。 */
  loop?: boolean
}

/**
 * CustomBehavior 自定义行为引擎。
 * 按配置中的步骤序列依次执行动作。
 */
export class CustomBehavior extends Behavior {
  private config: CustomBehaviorConfig
  private currentStep = 0
  private waitUntil = 0
  private mover: PathfinderMover | null = null
  private moverInitialized = false

  constructor(botId: string, config: CustomBehaviorConfig) {
    super(botId)
    this.config = config
  }

  get name() { return 'custom' }

  start(): void {
    super.start()
    this.currentStep = 0
    this.waitUntil = 0
  }

  stop(): void {
    super.stop()
    if (this.mover) {
      this.mover.stop()
    }
  }

  /** 确保 pathfinder 已初始化。 */
  private async ensureMover(): Promise<void> {
    if (this.moverInitialized || !this.mcBot) return
    this.mover = new PathfinderMover(this.mcBot)
    await this.mover.init()
    this.moverInitialized = true
  }

  async tick(): Promise<void> {
    if (!this.running || !this.mcBot) return
    const steps = this.config.steps
    if (steps.length === 0) return

    await this.ensureMover()

    // 等待中
    if (Date.now() < this.waitUntil) return

    const step = steps[this.currentStep]

    try {
      await this.executeStep(step)
    } catch (err) {
      sendEvent({
        evt: 'bot-error',
        botId: this.botId,
        error: `custom 步骤 ${this.currentStep} 执行失败: ${err}`,
      })
    }

    // 推进到下一步
    this.currentStep++
    if (this.currentStep >= steps.length) {
      if (this.config.loop) {
        this.currentStep = 0
      } else {
        sendEvent({
          evt: 'bot-event',
          botId: this.botId,
          type: 'custom-complete',
          data: { steps: steps.length },
        })
        this.running = false
      }
    }
  }

  /** 执行单个步骤。 */
  private async executeStep(step: BehaviorStep): Promise<void> {
    if (!this.mcBot) return

    switch (step.type) {
      case 'move':
        if (step.pos && this.mover?.isReady()) {
          await this.mover.moveTo(step.pos.x, step.pos.y, step.pos.z)
        }
        break

      case 'chat':
        if (step.message) {
          this.mcBot.chat(step.message)
        }
        break

      case 'wait':
        this.waitUntil = Date.now() + (step.duration || 1000)
        break

      case 'attack': {
        const hostile = this.mcBot.nearestEntity(
          (e) => e.kind === 'Hostile mobs'
        )
        if (hostile) {
          this.mcBot.attack(hostile)
        }
        break
      }

      case 'interact':
        if (step.pos) {
          const block = this.mcBot.blockAt(
            // @ts-expect-error Vec3 兼容
            { x: step.pos.x, y: step.pos.y, z: step.pos.z }
          )
          if (block) {
            await this.mcBot.activateBlock(block)
          }
        }
        break

      case 'use_item':
        this.mcBot.activateItem(false)
        break
    }
  }
}
