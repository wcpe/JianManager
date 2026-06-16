/**
 * Bot Worker 入口。
 * 通过 stdin/stdout JSON 行协议与 Worker Node 通信。
 *
 * 启动参数：
 *   --prewarm=N  预热 N 个空闲 Bot（默认 0）
 */

import { createInterface } from 'readline'
import { handleCommand, init, initPrewarm } from './ipc/handler.js'
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

// 解析启动参数
const prewarmArg = process.argv.find((a) => a.startsWith('--prewarm='))
const prewarmCount = prewarmArg ? parseInt(prewarmArg.split('=')[1], 10) || 0 : 0

// 初始化子系统
init()
if (prewarmCount > 0) {
  initPrewarm(prewarmCount)
}

sendEvent({ evt: 'worker-ready' })
