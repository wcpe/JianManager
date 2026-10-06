import api from '@/api/client'
import type { ClientFileResult } from '@/api/clientVersions'

// 纯逻辑（切片数学、进度归并、abortError、类型）已迁至 `@jianmanager/ui`；
// 此处转出，调用点零改动。
export * from '@jianmanager/ui/lib/chunked-upload'

import { abortError, progressBytes, sliceRanges } from '@jianmanager/ui/lib/chunked-upload'

/**
 * 客户端分发大文件分块上传客户端（FR-251，增强 FR-088）。
 *
 * 复用后端分块协议：init（声明大小得 uploadId/chunkSize/chunkCount）→ 按 chunkSize 顺序
 * PUT 各分片原始字节（幂等）→ complete（服务端拼装喂 CAS，返回与单次上传一致的
 * {sha256,md5,size,codec}）。onProgress 报已上传/总字节；signal 支持取消（取消即 DELETE 弃单）。
 *
 * 签名对 FR-250（延迟批量上传编排）稳定：uploadFileChunked(channelId,file,{onProgress,signal})。
 *
 * 网络部分留应用侧（要读 `@/api/client` 与 `ClientFileResult`），纯逻辑取自包内。
 */

/** init 返回（对应后端 service.InitResult）。 */
interface InitUploadResult {
  uploadId: string
  chunkSize: number
  chunkCount: number
}

/**
 * 分块上传一个文件到指定频道，返回内容寻址元数据（与单次上传 usePublishClientFile 同结构）。
 *
 * 流程：init → 顺序 PUT 各分片（application/octet-stream 原始字节）→ complete。
 * 0 字节文件（.gitkeep/空配置）：init(totalSize=0) 合法、无分片，直达 complete（后端落空内容 CAS）。
 * 取消（signal.aborted）在任意阶段生效：已 init 则 best-effort DELETE 弃单后抛 AbortError。
 * codec 恒 none（本期发布不压缩，与既有 ClientPublishPage 一致）。
 */
export async function uploadFileChunked(
  channelId: string,
  file: File,
  opts: import('@jianmanager/ui/lib/chunked-upload').UploadFileChunkedOptions = {},
): Promise<ClientFileResult> {
  const { onProgress, signal, chunkSize: wantChunkSize, expectedSha256 } = opts

  if (signal?.aborted) throw abortError()

  // 1) init：声明文件大小，取服务端敲定的 chunkSize/chunkCount。
  const { data: init } = await api.post<InitUploadResult>(
    `/client-channels/${channelId}/uploads`,
    { filename: file.name, totalSize: file.size, chunkSize: wantChunkSize },
    { signal },
  )

  const ranges = sliceRanges(file.size, init.chunkSize)

  try {
    // 2) 顺序 PUT 各分片（原始字节切片）。片粒度进度：每片完成后回调。
    // 分片默认吃 10s 全局超时——慢上行传 8MiB 轻易超 10s 即整单报废（「上传过大文件报错」）。
    // 单片放宽到 5 分钟（8MiB @ ≥0.03MB/s 均可完成），complete 同理（服务端拼装大文件耗时）。
    for (const r of ranges) {
      if (signal?.aborted) throw abortError()
      const blob = file.slice(r.start, r.end)
      await api.put(
        `/client-channels/${channelId}/uploads/${init.uploadId}/chunks/${r.index}`,
        blob,
        { headers: { 'Content-Type': 'application/octet-stream' }, signal, timeout: 300_000 },
      )
      onProgress?.(progressBytes(r.index + 1, init.chunkSize, file.size), file.size)
    }
    // 空文件（size=0，无分片）也回一次 0/0 进度，保证 UI 有终态。
    if (ranges.length === 0) onProgress?.(0, file.size)

    if (signal?.aborted) throw abortError()

    // 3) complete：服务端校验齐全 + 拼装喂 CAS，返回内容寻址元数据。
    // 已知原始内容 sha256 时顺带强校验（FR-346；服务端既有 expectedSha256 能力）。
    const { data: result } = await api.post<ClientFileResult>(
      `/client-channels/${channelId}/uploads/${init.uploadId}/complete`,
      expectedSha256 ? { codec: 'none', expectedSha256 } : { codec: 'none' },
      { signal, timeout: 300_000 },
    )
    return result
  } catch (err) {
    // 取消或任何失败：best-effort 弃单（DELETE），清理服务端临时分片。弃单本身失败不掩盖原错误。
    await abortUpload(channelId, init.uploadId)
    throw err
  }
}

/** best-effort 弃单：向服务端 DELETE 上传会话；失败静默（TTL 会兜底清理）。 */
async function abortUpload(channelId: string, uploadId: string): Promise<void> {
  try {
    await api.delete(`/client-channels/${channelId}/uploads/${uploadId}`)
  } catch {
    // 弃单失败不阻断：服务端 TTL 会回收空闲会话的临时分片。
  }
}
