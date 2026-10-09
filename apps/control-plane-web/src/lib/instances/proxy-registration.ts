/**
 * 代理后端注册契约（FR-035）。
 *
 * 归包理由：受控视图要按注册项渲染后端名/别名/优先级/强制域名，并产出注册载荷、
 * 消费 resync 结果，这套结构是双侧共用的；应用侧 API 层改从包 import，避免两处各写一份。
 */

/** 一条代理后端注册关系。 */
export interface ProxyRegistration {
  id: number
  proxyId: number
  backendId: number
  alias: string
  priority: number
  forcedHost: string
  restricted: boolean
  enabled: boolean
  /** 后端实例摘要（由后端在响应里回填，列表直接显示名字，不再逐个查实例）。 */
  backend?: {
    id: number
    name: string
    role: string
    nodeId: number
    serverPort: number
    status: string
  }
}

/** 注册后端的载荷。 */
export interface RegisterProxyBackendPayload {
  backendId: number
  alias?: string
  forcedHost?: string
  restricted: boolean
}

/** 重新同步结果：secret 跨代理不一致与逐条警告由视图决定怎么提示。 */
export interface ProxyResyncResult {
  secretConsistent?: boolean
  warnings?: string[]
}
