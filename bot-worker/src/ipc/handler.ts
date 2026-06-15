/**
 * IPC 命令分发器。
 */

import { sendEvent } from '../index.js'
import type { IpcCommand } from './types.js'

/** 处理来自 Worker Node 的命令。 */
export function handleCommand(cmd: IpcCommand): void {
  switch (cmd.cmd) {
    case 'create-bots':
      // TODO: 实现 Bot 创建
      sendEvent({ evt: 'bot-state', bots: cmd.bots.map(b => ({ id: b.id, status: 'pending' })) })
      break
    case 'stop-bots':
      // TODO: 实现 Bot 停止
      sendEvent({ evt: 'bot-state', bots: cmd.botIds.map(id => ({ id, status: 'stopped' })) })
      break
    case 'set-behavior':
      // TODO: 实现行为切换
      sendEvent({ evt: 'bot-event', botId: cmd.botId, type: 'behavior-changed', data: { behavior: cmd.behavior } })
      break
    case 'send-command':
      // TODO: 实现命令发送
      sendEvent({ evt: 'bot-event', botId: cmd.botId, type: 'command-sent', data: { command: cmd.command } })
      break
    default:
      sendEvent({ evt: 'bot-error', error: `未知命令: ${(cmd as { cmd: string }).cmd}` })
  }
}
