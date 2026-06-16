/**
 * IPC 命令分发器。
 * 管理 Bot 实例和行为引擎，通过 Mineflayer 连接 MC 服务器。
 */

import { createBot, type Bot } from 'mineflayer'
import { sendEvent } from '../index.js'
import { createBehavior, type Behavior } from '../behavior/index.js'
import type { IpcCommand, BotConfig } from './types.js'

/** 活跃的 Bot 实例。 */
interface BotInstance {
  config: BotConfig
  behavior: Behavior
  status: string
  mcBot: Bot | null
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

    const instance: BotInstance = {
      config,
      behavior,
      status: 'connecting',
      mcBot: null,
    }
    bots.set(config.id, instance)

    results.push({ id: config.id, status: 'connecting' })

    // 通过 Mineflayer 连接到 MC 服务器
    connectBot(config.id, config)
  }

  sendEvent({ evt: 'bot-state', bots: results })
}

/** 通过 Mineflayer 连接到 MC 服务器。 */
function connectBot(botId: string, config: BotConfig): void {
  try {
    const mcBot = createBot({
      host: config.host,
      port: config.port || 25565,
      username: config.username || `Bot_${botId.slice(0, 6)}`,
      version: config.version,
      hideErrors: true,
    })

    const instance = bots.get(botId)
    if (!instance) return
    instance.mcBot = mcBot

    mcBot.on('spawn', () => {
      if (instance) {
        instance.status = 'connected'
      }
      sendEvent({ evt: 'bot-state', bots: [{ id: botId, status: 'connected' }] })
      sendEvent({ evt: 'bot-event', botId, type: 'spawn', data: {} })
    })

    mcBot.on('kicked', (reason: string) => {
      if (instance) {
        instance.status = 'disconnected'
      }
      sendEvent({ evt: 'bot-state', bots: [{ id: botId, status: 'disconnected' }] })
      sendEvent({ evt: 'bot-event', botId, type: 'kicked', data: { reason } })
    })

    mcBot.on('error', (err: Error) => {
      sendEvent({ evt: 'bot-error', botId, error: err.message })
    })

    mcBot.on('end', () => {
      if (instance) {
        instance.status = 'disconnected'
      }
      sendEvent({ evt: 'bot-state', bots: [{ id: botId, status: 'disconnected' }] })
    })

    // 行为引擎 tick 循环
    const tickInterval = setInterval(() => {
      if (!bots.has(botId)) {
        clearInterval(tickInterval)
        return
      }
      const inst = bots.get(botId)
      if (inst && inst.status === 'connected') {
        inst.behavior.tick().catch((err: Error) => {
          sendEvent({ evt: 'bot-error', botId, error: err.message })
        })
      }
    }, 250)
  } catch (err) {
    const instance = bots.get(botId)
    if (instance) {
      instance.status = 'error'
    }
    sendEvent({ evt: 'bot-error', botId, error: String(err) })
  }
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
    if (instance.mcBot) {
      instance.mcBot.quit()
      instance.mcBot = null
    }
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

  if (!instance.mcBot) {
    sendEvent({ evt: 'bot-error', botId, error: `Bot ${botId} 未连接到 MC 服务器` })
    return
  }

  // 通过 Mineflayer 发送聊天命令
  instance.mcBot.chat(command)

  sendEvent({
    evt: 'bot-event',
    botId,
    type: 'command-sent',
    data: { command },
  })
}
