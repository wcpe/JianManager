/**
 * IPC 消息类型定义。
 * Bot Worker 通过 stdin/stdout JSON 行协议与 Worker Node 通信。
 */

/** Worker Node → Bot Worker 的命令。 */
export type IpcCommand =
  | CreateBotsCommand
  | StopBotsCommand
  | SetBehaviorCommand
  | SendBotCommand

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
}

/** 向 Bot 发送命令。 */
export interface SendBotCommand {
  cmd: 'send-command'
  botId: string
  command: string
}

/** Bot 配置。 */
export interface BotConfig {
  id: string
  name: string
  server: string
  port: number
  auth: 'offline' | 'microsoft'
  behavior?: string
}
