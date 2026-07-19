/**
 * IPC 命令分发器。
 * FleetController 负责批量准入和连接生命周期，本文件保留旧行为、命令与脚本入口。
 */

import { randomUUID } from 'node:crypto'
import { createBot } from 'mineflayer'
import { sendEvent } from '../index.js'
import { createBehavior } from '../behavior/index.js'
import { ScriptRunner } from '../script/index.js'
import { PrewarmPool } from '../state/prewarm.js'
import { StateReporter } from '../state/index.js'
import { HealthCheck } from '../health/index.js'
import { FleetController } from './fleet.js'
import type { BehaviorConfig, IpcCommand } from './types.js'

const MAX_BOTS = 50
const BOT_WORKER_VERSION = '0.4.0'

const prewarmPool = new PrewarmPool({ count: 0, maxPoolSize: MAX_BOTS })
const scriptRunner = new ScriptRunner()
const stateReporter = new StateReporter({ intervalMs: 3000 })
const workerEpochGeneration = parseWorkerEpochGeneration()

const fleet = new FleetController({
  maxBots: MAX_BOTS,
  workerEpoch: randomUUID(),
  workerEpochGeneration,
  sendEvent,
  createBot: (options) => createBot(options as unknown as Parameters<typeof createBot>[0]),
  createBehavior: (botId, behavior, config) => createBehavior(
    botId,
    behavior,
    config as BehaviorConfig | string | undefined
  ),
  onBotStopped: () => prewarmPool.add(),
})

const healthCheck = new HealthCheck({
  intervalMs: 10000,
  fleetHeartbeat: (eventLoopP95Ms) => fleet.heartbeat(eventLoopP95Ms),
})

/** 初始化周期状态与心跳上报。 */
export function init(): void {
  stateReporter.setSnapshotProvider(() => fleet.snapshots())
  stateReporter.start()
  healthCheck.start()
}

/** 初始化预热池。 */
export function initPrewarm(count: number): void {
  prewarmPool.init(count)
}

/** 处理来自 Worker Node 的命令。 */
export function handleCommand(command: IpcCommand): void {
  if (fleet.handleCommand(command)) return

  switch (command.cmd) {
    case 'set-behavior':
      setBehavior(command.botId, command.behavior, command.target, command.config)
      break
    case 'send-command':
      sendBotCommand(command.botId, command.command)
      break
    case 'run-script':
      scriptRunner.execute(
        command.scriptId,
        command.steps,
        command.botIds,
        (botId) => fleet.getBot(botId) as never
      )
      break
    case 'stop-script':
      scriptRunner.stop()
      break
    default:
      sendEvent({ evt: 'bot-error', error: `未知命令: ${(command as { cmd: string }).cmd}` })
  }
}

/** 返回进程启动能力事件。 */
export function getWorkerReadyEvent(): object {
  return fleet.workerReady(BOT_WORKER_VERSION)
}

/** 停止所有子系统并取消延迟连接。 */
export function shutdown(): void {
  stateReporter.stop()
  healthCheck.stop()
  scriptRunner.stop()
  fleet.shutdown()
}

function setBehavior(
  botId: string,
  behaviorType: string,
  target?: string,
  config?: BehaviorConfig
): void {
  const result = fleet.setBehavior(botId, behaviorType, target, config)
  if (result === 'missing') {
    sendEvent({ evt: 'bot-error', botId, error: `Bot ${botId} 不存在` })
    return
  }
  if (result === 'fleet_managed') {
    sendEvent({ evt: 'bot-error', botId, errorCode: 'fleet_managed', error: `Bot ${botId} 由 Fleet 场景管理` })
    return
  }
  sendEvent({
    evt: 'bot-event',
    botId,
    type: 'behavior-changed',
    data: { behavior: behaviorType, target },
  })
}

function sendBotCommand(botId: string, command: string): void {
  const result = fleet.sendBotCommand(botId, command)
  if (result === 'missing') {
    sendEvent({ evt: 'bot-error', botId, error: `Bot ${botId} 不存在` })
    return
  }
  if (result === 'disconnected') {
    sendEvent({ evt: 'bot-error', botId, error: `Bot ${botId} 未连接到 MC 服务器` })
    return
  }
  sendEvent({ evt: 'bot-event', botId, type: 'command-sent', data: { command } })
}

/** 获取预热池状态。 */
export function getPrewarmStats() {
  return prewarmPool.stats()
}

/** 获取容量信息。 */
export function getCapacityInfo() {
  const metrics = fleet.metrics()
  return {
    current: metrics.activeBots,
    max: metrics.maxBots,
    remaining: Math.max(0, metrics.maxBots - metrics.activeBots),
  }
}

function parseWorkerEpochGeneration(): number {
  const argument = process.argv.find((item) => item.startsWith('--worker-epoch-generation='))
  if (!argument) return 1
  const value = Number.parseInt(argument.split('=')[1], 10)
  return Number.isFinite(value) && value > 0 ? value : 1
}
