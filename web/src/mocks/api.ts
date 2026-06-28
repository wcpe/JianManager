/**
 * mock 传输层路径助手（FR-196）。
 * axios 客户端 baseURL=/api/v1（见 web/src/api/client.ts）。
 * 用 `*` 通配 origin：MSW 在 node（无 location）下 relative 路径不匹配，`* /api/v1/...`
 * 可同时匹配 node 测试 / jsdom / 浏览器 mock 模式（:5173）任意 origin（已实测验证）。
 */
export const API = (path: string): string => `*/api/v1${path}`
