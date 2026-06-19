/**
 * IPC 命令分发器。
 * 管理 Bot 实例和行为引擎，通过 Mineflayer 连接 MC 服务器。
 *
 * 功能：
 *   - 批量创建/停止 Bot
 *   - 行为模式切换（idle/follow/patrol/guard/custom）
 *   - 脚本执行 + 进度上报
 *   - 预热池管理
 *   - 容量限制（50 bots/worker）
 *   - 周期性状态上报
 */

import { createBot, type Bot } from 'mineflayer'
import { sendEvent } from '../index.js'
import { createBehavior, type Behavior, type CustomBehaviorConfig } from '../behavior/index.js'
import { ScriptRunner } from '../script/index.js'
import { PrewarmPool } from '../state/prewarm.js'
import { CapacityLimiter } from '../state/capacity.js'
import { StateReporter, type BotStateSnapshot } from '../state/index.js'
import { HealthCheck } from '../health/index.js'
import type { IpcCommand, BotConfig } from './types.js'

/** 活跃的 Bot 实例。 */
interface BotInstance {
  config: BotConfig
  behavior: Behavior
  status: string
  mcBot: Bot | null
}

const bots = new Map<string, BotInstance>()

/** 预热池（默认 5 个预热，最大 50）。 */
const prewarmPool = new PrewarmPool({ count: 0, maxPoolSize: 50 })

/** 容量限制器（默认 50 bots/worker）。 */
const capacity = new CapacityLimiter({ maxBots: 50 })

/** 脚本执行器。 */
const scriptRunner = new ScriptRunner()

/** 状态上报器（每 3s 上报一次）。 */
const stateReporter = new StateReporter({ intervalMs: 3000 })

/** 心跳检测器（每 10s 发送一次）。 */
const healthCheck = new HealthCheck({ intervalMs: 10000 })

/** 初始化子系统。 */
export function init(): void {
  stateReporter.setSnapshotProvider(() => {
    const snapshots: BotStateSnapshot[] = []
    for (const [id, instance] of bots) {
      const snapshot: BotStateSnapshot = {
        id,
        status: instance.status,
        name: instance.config.name,
        behavior: instance.behavior.name,
      }
      if (instance.mcBot && instance.status === 'connected') {
        snapshot.health = instance.mcBot.health
        snapshot.food = instance.mcBot.food
        const pos = instance.mcBot.entity?.position
        if (pos) {
          snapshot.position = { x: pos.x, y: pos.y, z: pos.z }
        }
      }
      snapshots.push(snapshot)
    }
    return snapshots
  })

  stateReporter.start()
  healthCheck.start()
}

/** 初始化预热池。 */
export function initPrewarm(count: number): void {
  prewarmPool.init(count)
}

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
      setBehavior(cmd.botId, cmd.behavior, cmd.target, cmd.config)
      break
    case 'send-command':
      sendBotCommand(cmd.botId, cmd.command)
      break
    case 'run-script':
      scriptRunner.execute(
        cmd.scriptId,
        cmd.steps,
        cmd.botIds,
        (botId) => bots.get(botId)?.mcBot ?? null
      )
      break
    case 'stop-script':
      scriptRunner.stop()
      break
    default:
      sendEvent({ evt: 'bot-error', error: `未知命令: ${(cmd as { cmd: string }).cmd}` })
  }
}

/** 批量创建 Bot。 */
function createBots(configs: BotConfig[]): void {
  if (!capacity.canCreate(configs.length)) {
    sendEvent({
      evt: 'bot-error',
      error: `容量超限：当前 ${capacity.current()}，请求 ${configs.length}，上限 ${capacity.max()}`,
    })
    return
  }

  const results: Array<{ id: string; status: string }> = []

  for (const config of configs) {
    // 已存在（可能已断开）：停掉旧连接后按新配置重建，实现「重连」语义。
    // 此前直接返回 already_exists 导致断开的 Bot 永远连不回来。
    const existing = bots.get(config.id)
    if (existing) {
      existing.behavior.stop()
      if (existing.mcBot) {
        try {
          existing.mcBot.quit()
        } catch {
          // 已断开，忽略
        }
      }
      bots.delete(config.id)
      capacity.remove(1)
    }

    const behavior = createBehavior(config.id, config.behavior || 'idle', config.behaviorConfig)
    behavior.start()

    const instance: BotInstance = {
      config,
      behavior,
      status: 'connecting',
      mcBot: null,
    }
    bots.set(config.id, instance)
    capacity.add(1)

    results.push({ id: config.id, status: 'connecting' })

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
        inst.behavior.setMcBot(mcBot)
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
    capacity.remove(1)
    prewarmPool.add()
    results.push({ id, status: 'stopped' })
  }

  sendEvent({ evt: 'bot-state', bots: results })
}

/** 切换 Bot 行为。 */
function setBehavior(
  botId: string,
  behaviorType: string,
  target?: string,
  config?: CustomBehaviorConfig
): void {
  const instance = bots.get(botId)
  if (!instance) {
    sendEvent({ evt: 'bot-error', botId, error: `Bot ${botId} 不存在` })
    return
  }

  instance.behavior.stop()

  const newBehavior = behaviorType === 'custom' && config
    ? createBehavior(botId, 'custom', config)
    : createBehavior(botId, behaviorType, target)

  if (instance.mcBot) {
    newBehavior.setMcBot(instance.mcBot)
  }

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

  instance.mcBot.chat(command)

  sendEvent({
    evt: 'bot-event',
    botId,
    type: 'command-sent',
    data: { command },
  })
}

/** 获取预热池状态。 */
export function getPrewarmStats() {
  return prewarmPool.stats()
}

/** 获取容量信息。 */
export function getCapacityInfo() {
  return {
    current: capacity.current(),
    max: capacity.max(),
    remaining: capacity.remaining(),
  }
}
