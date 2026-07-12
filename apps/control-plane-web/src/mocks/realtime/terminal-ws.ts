import { ws, HttpResponse } from 'msw'
import { domainRoute } from '@/mocks/inject'

/**
 * 终端实时流仿真（FR-198）。
 * token handler 走 HTTP（node 测试可验）；WS 伪交互仅并入浏览器 mock 模式（browser.ts）——
 * jsdom 下 WebSocket 不稳，按 ADR-047/spec §6 退路：终端交互真机验收，jsdom 只测 token handler。
 */
const MOCK_TERMINAL_URL = 'ws://localhost/_mock/terminal'

/** GET /instances/:id/terminal-token：返回 mock WS 地址（FR-198）。FR-201 实例域不重定义本路由。 */
export const terminalTokenHandler = domainRoute('get', '/instances/:id/terminal-token', () =>
  HttpResponse.json({ token: 'mock-terminal-token', wsUrl: MOCK_TERMINAL_URL, expiresIn: 30 }),
)

const terminal = ws.link(MOCK_TERMINAL_URL)

/** 终端 WS PTY 伪交互：连接发 banner，收 {type:'stdin'} 回 {type:'stdout'} 假输出（FR-198）。 */
export const terminalWsHandler = terminal.addEventListener('connection', ({ client }) => {
  client.send(JSON.stringify({ type: 'stdout', data: '[mock 终端已连接]\r\n' }))
  client.addEventListener('message', (event) => {
    let msg: { type?: string; data?: string }
    try {
      msg = JSON.parse(String(event.data)) as { type?: string; data?: string }
    } catch {
      return
    }
    if (msg.type !== 'stdin') return
    const cmd = (msg.data ?? '').trim()
    if (cmd === 'list') {
      client.send(JSON.stringify({ type: 'stdout', data: 'There are 2 of a max of 20 players online: admin, operator\r\n' }))
    } else if (cmd === 'stop') {
      client.send(JSON.stringify({ type: 'stdout', data: 'Stopping the server\r\n' }))
      client.send(JSON.stringify({ type: 'state', state: 'STOPPED' }))
    } else {
      client.send(JSON.stringify({ type: 'stdout', data: `[mock] 已执行: ${cmd}\r\n` }))
    }
  })
})
