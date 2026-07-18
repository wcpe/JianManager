/**
 * Bot Worker 入口。
 * 通过 stdin/stdout JSON 行协议与 Worker Node 通信。
 */

import { createInterface } from 'node:readline'
import {
  getWorkerReadyEvent,
  handleCommand,
  init,
  initPrewarm,
  shutdown,
} from './ipc/handler.js'
import type { IpcCommand } from './ipc/types.js'

const rl = createInterface({ input: process.stdin })

rl.on('line', (line: string) => {
  try {
    handleCommand(JSON.parse(line) as IpcCommand)
  } catch {
    sendEvent({ evt: 'bot-error', error: `无效的 JSON 消息: ${line}` })
  }
})

/** 向 Worker Node 发送事件。 */
export function sendEvent(event: object): void {
  process.stdout.write(`${JSON.stringify(event)}\n`)
}

const prewarmArgument = process.argv.find((item) => item.startsWith('--prewarm='))
const prewarmCount = prewarmArgument ? Number.parseInt(prewarmArgument.split('=')[1], 10) || 0 : 0

init()
if (prewarmCount > 0) initPrewarm(prewarmCount)

let closing = false
function close(): void {
  if (closing) return
  closing = true
  rl.close()
  shutdown()
}

process.once('SIGTERM', close)
process.once('SIGINT', close)
process.once('beforeExit', close)

sendEvent(getWorkerReadyEvent())
