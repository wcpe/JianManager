/**
 * 脚本执行器。
 * 接受 run-script IPC 命令，执行用户定义的脚本并上报进度。
 *
 * 脚本格式：JSON 行，每行是一个步骤指令。
 * 支持的指令：chat, move, wait, command, log。
 */

import { sendEvent } from '../index.js'

/** 脚本步骤。 */
interface ScriptStep {
  /** 指令类型。 */
  action: 'chat' | 'move' | 'wait' | 'command' | 'log'
  /** 聊天内容（chat）。 */
  message?: string
  /** 坐标（move）。 */
  pos?: { x: number; y: number; z: number }
  /** 等待毫秒（wait）。 */
  duration?: number
  /** 控制台命令（command）。 */
  command?: string
  /** 日志消息（log）。 */
  text?: string
}

/** 脚本执行器。 */
export class ScriptRunner {
  private running = false
  private currentStep = 0
  private totalSteps = 0
  private scriptId = ''

  /** 是否正在执行脚本。 */
  isRunning(): boolean {
    return this.running
  }

  /**
   * 执行脚本。
   * @param scriptId 脚本标识。
   * @param steps 脚本步骤列表。
   * @param botIds 执行脚本的 Bot ID 列表。
   * @param botAccessor 获取 Bot mcBot 实例的函数。
   */
  async execute(
    scriptId: string,
    steps: ScriptStep[],
    botIds: string[],
    botAccessor: (botId: string) => import('mineflayer').Bot | null
  ): Promise<void> {
    if (this.running) {
      sendEvent({ evt: 'bot-error', error: '已有脚本正在执行' })
      return
    }

    this.scriptId = scriptId
    this.running = true
    this.currentStep = 0
    this.totalSteps = steps.length * botIds.length

    sendEvent({
      evt: 'script-progress',
      scriptId,
      progress: 0,
      total: this.totalSteps,
      status: 'started',
    })

    try {
      for (const botId of botIds) {
        if (!this.running) break

        const mcBot = botAccessor(botId)
        if (!mcBot) {
          sendEvent({
            evt: 'bot-error',
            botId,
            error: `脚本执行：Bot ${botId} 未连接`,
          })
          continue
        }

        for (const step of steps) {
          if (!this.running) break

          await this.executeStep(mcBot, step)
          this.currentStep++

          sendEvent({
            evt: 'script-progress',
            scriptId,
            botId,
            progress: this.currentStep,
            total: this.totalSteps,
            status: 'running',
            step: step.action,
          })
        }
      }

      sendEvent({
        evt: 'script-progress',
        scriptId,
        progress: this.totalSteps,
        total: this.totalSteps,
        status: 'completed',
      })
    } catch (err) {
      sendEvent({
        evt: 'script-progress',
        scriptId,
        progress: this.currentStep,
        total: this.totalSteps,
        status: 'error',
        error: String(err),
      })
    } finally {
      this.running = false
    }
  }

  /** 停止正在执行的脚本。 */
  stop(): void {
    this.running = false
    sendEvent({
      evt: 'script-progress',
      scriptId: this.scriptId,
      progress: this.currentStep,
      total: this.totalSteps,
      status: 'cancelled',
    })
  }

  /** 执行单个步骤。 */
  private async executeStep(
    mcBot: import('mineflayer').Bot,
    step: ScriptStep
  ): Promise<void> {
    switch (step.action) {
      case 'chat':
        if (step.message) {
          mcBot.chat(step.message)
        }
        break

      case 'move':
        if (step.pos) {
          // 使用 pathfinder 或简单移动
          const pf = (mcBot as unknown as { pathfinder?: { setGoal: (g: unknown) => void } }).pathfinder
          if (pf) {
            const { goals } = await import('mineflayer-pathfinder')
            pf.setGoal(new goals.GoalBlock(step.pos.x, step.pos.y, step.pos.z))
          }
        }
        break

      case 'wait':
        await new Promise((resolve) => setTimeout(resolve, step.duration || 1000))
        break

      case 'command':
        if (step.command) {
          mcBot.chat(step.command)
        }
        break

      case 'log':
        sendEvent({
          evt: 'bot-event',
          type: 'script-log',
          data: { text: step.text || '', scriptId: this.scriptId },
        })
        break
    }
  }
}
