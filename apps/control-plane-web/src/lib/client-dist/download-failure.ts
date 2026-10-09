/**
 * 判定任务/操作错误是否为「出站网络不可达」类失败（FR-279）。
 *
 * Worker 侧对 JDK 等下载的网络类失败已追加中文可操作引导，同时**保留底层 Go stdlib 英文标记**
 * （如 `TLS handshake timeout`）。前端据这些稳定标记识别网络类失败，渲染「去配置代理 / 换镜像源」入口。
 * 只匹配出站连接类症状，避免把 HTTP 4xx/5xx、磁盘、解压类错误误判为网络问题。
 */
const NETWORK_FAILURE_MARKERS = [
  'tls handshake timeout',
  'i/o timeout',
  'context deadline exceeded',
  'deadline exceeded',
  'connection refused',
  'connection reset',
  'no such host',
  'network is unreachable',
  'unexpected eof',
  'dial tcp',
  // Worker 追加的中文引导本身也含「网络受限」，兜底命中（即便未来英文标记变化）。
  '疑似网络受限',
]

/** 错误文本是否为出站网络不可达类失败。空/非字符串返回 false。 */
export function isNetworkDownloadFailure(errText: string | null | undefined): boolean {
  if (!errText) return false
  const lower = errText.toLowerCase()
  return NETWORK_FAILURE_MARKERS.some((m) => lower.includes(m))
}
