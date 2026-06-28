import { http, HttpResponse } from 'msw'
import { API } from '@/mocks/api'

/**
 * 实例事件 SSE 流仿真（FR-198）。
 * web/src/api/events.ts 用 fetch+ReadableStream 消费 `event: instance` / `data: {...}`，
 * 收到 `type==='state_change'` 且含 `instanceUuid` 即失效 ['instances'] query。
 */
const encoder = new TextEncoder()
const controllers = new Set<ReadableStreamDefaultController<Uint8Array>>()

/** 向所有已连接的 /instances/events 客户端推送一条实例事件。测试 / mock 模式触发联动。 */
export function emitInstanceEvent(payload: { type: string; instanceUuid: string; [k: string]: unknown }): void {
  const bytes = encoder.encode(`event: instance\ndata: ${JSON.stringify(payload)}\n\n`)
  for (const c of controllers) {
    try {
      c.enqueue(bytes)
    } catch {
      controllers.delete(c)
    }
  }
}

export const instanceEventsHandlers = [
  http.get(API('/instances/events'), () => {
    let ref: ReadableStreamDefaultController<Uint8Array>
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        ref = controller
        controllers.add(controller)
      },
      cancel() {
        controllers.delete(ref)
      },
    })
    return new HttpResponse(stream, {
      headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' },
    })
  }),
]
