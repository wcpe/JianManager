import { http, HttpResponse, delay, type HttpResponseResolver, type HttpHandler } from 'msw'
import { API } from './api'
import { mockLatencyDelay } from './runtime-control'

/**
 * 错误注入框架（FR-197 / ADR-047 决策 6）。
 * 「成功默认铺满 + 按需注入错误」：handler 默认走 resolver 返回成功，
 * 测试 / mock 模式可对指定 (method, pattern) 注入错误码 / 空态 / 网络错误 / 延迟。
 */
export type Scenario =
  | { kind: 'status'; status: number; body?: unknown }
  | { kind: 'empty' }
  | { kind: 'network' }
  | { kind: 'delay'; ms: number; then?: Scenario }

const overrides = new Map<string, Scenario>()
const keyOf = (method: string, pattern: string) => `${method.toUpperCase()} ${pattern}`

/** 对某 endpoint 注入一种情况（pattern 同 domainRoute 注册时的 pattern）。 */
export function mockInject(method: string, pattern: string, scenario: Scenario): void {
  overrides.set(keyOf(method, pattern), scenario)
}

/** 清空全部注入（测试 afterEach 调用）。 */
export function clearInjections(): void {
  overrides.clear()
}

async function applyScenario(s: Scenario): Promise<Response> {
  switch (s.kind) {
    case 'delay':
      await delay(s.ms)
      return s.then ? applyScenario(s.then) : HttpResponse.json({})
    case 'network':
      return HttpResponse.error()
    case 'empty':
      return HttpResponse.json([])
    case 'status':
      return HttpResponse.json(s.body ?? { error: 'INJECTED', message: '注入的模拟错误' }, { status: s.status })
  }
}

type Method = 'get' | 'post' | 'put' | 'patch' | 'delete'

/**
 * 注册一条域路由：先查注入覆盖，命中则返回注入响应，否则走 resolver 默认成功。
 * pattern 既是 MSW 路由（经 API() 加 /api/v1 前缀），也是 mockInject 的键。
 *
 * FR-496 阶段 6 补丁：这里也是**全局接口延迟的唯一注入点**。全部域路由都经本工厂注册，
 * 所以在 resolver 之前统一 `await mockLatencyDelay()` 就能一次性让所有接口（含后续新增域）
 * 受调试面板的延迟档位控制，且读的是运行时值——不需要为每个 handler 加一遍 `delay()`，
 * 也不需要为了改延迟重启 worker。少数不经本工厂的路由（如 `/instances/events` SSE 长连接、
 * `licenses.json` 静态文件）不参与限速，它们本就无「响应耗时」可调。
 */
export function domainRoute(method: Method, pattern: string, resolver: HttpResponseResolver): HttpHandler {
  return http[method](API(pattern), async (info) => {
    await mockLatencyDelay()
    const ov = overrides.get(keyOf(method, pattern))
    if (ov) return applyScenario(ov)
    return resolver(info)
  })
}
