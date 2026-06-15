/**
 * Bot Worker 入口。
 * 通过 stdin/stdout JSON 行协议与 Worker Node 通信。
 */

import { createInterface } from 'readline'
import { handleCommand } from './ipc/handler.js'
import type { IpcCommand } from './ipc/types.js'

const rl = createInterface({ input: process.stdin })

rl.on('line', (line: string) => {
  try {
    const cmd: IpcCommand = JSON.parse(line)
    handleCommand(cmd)
  } catch {
    sendEvent({ evt: 'bot-error', error: `无效的 JSON 消息: ${line}` })
  }
})

/** 向 Worker Node 发送事件。 */
export function sendEvent(event: Record<string, unknown>): void {
  process.stdout.write(JSON.stringify(event) + '\n')
}

sendEvent({ evt: 'worker-ready' })
