/**
 * IPC 消息类型定义。
 * Bot Worker 通过 stdin/stdout JSON 行协议与 Worker Node 通信。
 *
 * 命令类型（Go → Node.js）：
 *   create-bots   批量创建 Bot 连接
 *   stop-bots     批量停止 Bot
 *   set-behavior  切换行为模式
 *   send-command  向 Bot 发送聊天/控制命令
 *   run-script    执行脚本（带进度上报）
 *   stop-script   停止正在执行的脚本
 *
 * 事件类型（Node.js → Go）：
 *   bot-state     Bot 状态变更（连接/断开/错误）
 *   bot-event     Bot 事件（聊天/kicked/行为变更）
 *   bot-error     Bot 错误
 *   script-progress 脚本执行进度
 *   worker-ready  Bot Worker 进程就绪
 *   heartbeat     心跳
 */

import type { CustomBehaviorConfig } from '../behavior/custom.js'

/** Worker Node → Bot Worker 的命令。 */
export type IpcCommand =
  | CreateBotsCommand
  | StopBotsCommand
  | SetBehaviorCommand
  | SendBotCommand
  | RunScriptCommand
  | StopScriptCommand

/** 创建 Bot 命令。 */
export interface CreateBotsCommand {
  cmd: 'create-bots'
  bots: BotConfig[]
}

/** 停止 Bot 命令。 */
export interface StopBotsCommand {
  cmd: 'stop-bots'
  botIds: string[]
}

/** 切换行为模式命令。 */
export interface SetBehaviorCommand {
  cmd: 'set-behavior'
  botId: string
  behavior: string
  target?: string
  /** custom 行为的配置（behavior='custom' 时使用）。 */
  config?: CustomBehaviorConfig
}

/** 向 Bot 发送命令。 */
export interface SendBotCommand {
  cmd: 'send-command'
  botId: string
  command: string
}

/** 执行脚本命令。 */
export interface RunScriptCommand {
  cmd: 'run-script'
  scriptId: string
  /** 脚本步骤列表。 */
  steps: Array<{
    action: 'chat' | 'move' | 'wait' | 'command' | 'log'
    message?: string
    pos?: { x: number; y: number; z: number }
    duration?: number
    command?: string
    text?: string
  }>
  /** 执行脚本的 Bot ID 列表。 */
  botIds: string[]
}

/** 停止脚本命令。 */
export interface StopScriptCommand {
  cmd: 'stop-script'
  scriptId: string
}

/** Bot 配置。 */
export interface BotConfig {
  id: string
  name: string
  host: string
  port: number
  username?: string
  version?: string
  auth?: 'offline' | 'microsoft'
  behavior?: string
  /** custom 行为的配置。 */
  behaviorConfig?: CustomBehaviorConfig
  server?: string
}
