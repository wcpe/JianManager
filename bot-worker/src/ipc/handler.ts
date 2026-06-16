/**
 * IPC 命令分发器。
 * 管理 Bot 实例和行为引擎。
 */

import { sendEvent } from '../index.js'
import { createBehavior, type Behavior } from '../behavior/index.js'
import type { IpcCommand, BotConfig } from './types.js'

/** 活跃的 Bot 实例。 */
interface BotInstance {
  config: BotConfig
  behavior: Behavior
  status: string
}

const bots = new Map<string, BotInstance>()

/** 处理来自 Worker Node 的命令。 */
export function handleCommand(cmd: IpcCommand): void {
  switch (cmd.cmd) {
    case 'create-bots':
      createBots(cmd.bots)
      break
    case 'stop-bots':
      stopBots(cmd.botIds)
      break
    case 'set-behavior':
      setBehavior(cmd.botId, cmd.behavior, cmd.target)
      break
    case 'send-command':
      sendBotCommand(cmd.botId, cmd.command)
      break
    default:
      sendEvent({ evt: 'bot-error', error: `未知命令: ${(cmd as { cmd: string }).cmd}` })
  }
}

/** 批量创建 Bot。 */
function createBots(configs: BotConfig[]): void {
  const results: Array<{ id: string; status: string }> = []

  for (const config of configs) {
    if (bots.has(config.id)) {
      results.push({ id: config.id, status: 'already_exists' })
      continue
    }

    const behavior = createBehavior(config.id, config.behavior || 'idle')
    behavior.start()

    bots.set(config.id, {
      config,
      behavior,
      status: 'connecting',
    })

    results.push({ id: config.id, status: 'connecting' })

    // TODO: 使用 mineflayer 连接到 MC 服务器
    // const bot = mineflayer.createBot({ ... })
    // bot.on('spawn', () => { ... })
    // bot.on('kicked', () => { ... })

    // 模拟连接成功
    setTimeout(() => {
      const instance = bots.get(config.id)
      if (instance) {
        instance.status = 'connected'
        sendEvent({ evt: 'bot-state', bots: [{ id: config.id, status: 'connected' }] })
      }
    }, 1000)
  }

  sendEvent({ evt: 'bot-state', bots: results })
}

/** 停止 Bot。 */
function stopBots(botIds: string[]): void {
  const results: Array<{ id: string; status: string }> = []

  for (const id of botIds) {
    const instance = bots.get(id)
    if (!instance) {
      results.push({ id, status: 'not_found' })
      continue
    }

    instance.behavior.stop()
    bots.delete(id)
    results.push({ id, status: 'stopped' })
  }

  sendEvent({ evt: 'bot-state', bots: results })
}

/** 切换 Bot 行为。 */
function setBehavior(botId: string, behaviorType: string, target?: string): void {
  const instance = bots.get(botId)
  if (!instance) {
    sendEvent({ evt: 'bot-error', botId, error: `Bot ${botId} 不存在` })
    return
  }

  instance.behavior.stop()
  const newBehavior = createBehavior(botId, behaviorType, target)
  newBehavior.start()
  instance.behavior = newBehavior

  sendEvent({
    evt: 'bot-event',
    botId,
    type: 'behavior-changed',
    data: { behavior: behaviorType, target },
  })
}

/** 向 Bot 发送命令。 */
function sendBotCommand(botId: string, command: string): void {
  const instance = bots.get(botId)
  if (!instance) {
    sendEvent({ evt: 'bot-error', botId, error: `Bot ${botId} 不存在` })
    return
  }

  // TODO: 通过 mineflayer 执行命令
  sendEvent({
    evt: 'bot-event',
    botId,
    type: 'command-sent',
    data: { command },
  })
}
