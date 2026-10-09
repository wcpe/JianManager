import { describe, it, expect } from 'vitest'
import { isNetworkDownloadFailure } from './download-failure'

describe('isNetworkDownloadFailure（FR-279）', () => {
  it('命中 Go stdlib 网络类标记', () => {
    expect(isNetworkDownloadFailure('下载失败: Get "https://github.com/...": net/http: TLS handshake timeout')).toBe(true)
    expect(isNetworkDownloadFailure('dial tcp 1.2.3.4:443: connect: connection refused')).toBe(true)
    expect(isNetworkDownloadFailure('lookup x: no such host')).toBe(true)
    expect(isNetworkDownloadFailure('context deadline exceeded')).toBe(true)
    expect(isNetworkDownloadFailure('read: i/o timeout')).toBe(true)
  })
  it('命中 Worker 追加的中文引导兜底', () => {
    expect(isNetworkDownloadFailure('下载失败: xxx（疑似网络受限：JDK 下载经节点出站代理执行…）')).toBe(true)
  })
  it('本地类/业务类错误不误判', () => {
    expect(isNetworkDownloadFailure('已下载但未找到 bin/java，JDK 可能不完整')).toBe(false)
    expect(isNetworkDownloadFailure('下载返回 HTTP 404')).toBe(false)
    expect(isNetworkDownloadFailure('目标目录已存在: /opt/jdks/temurin-21')).toBe(false)
    expect(isNetworkDownloadFailure('')).toBe(false)
    expect(isNetworkDownloadFailure(null)).toBe(false)
    expect(isNetworkDownloadFailure(undefined)).toBe(false)
  })
})
