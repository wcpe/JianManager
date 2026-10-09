import api from '@/api/client'
import { precheckClientFiles, uploadClientFilesBatch } from '@/api/clientVersions'
import type { ClientFileResult } from '@/api/clientVersions'
import { abortError, progressBytes, sliceRanges } from './chunked-upload'
import type { UploadFileChunkedOptions } from './chunked-upload'
import { uploadFilesEfficient as runEfficientUpload } from './efficient-upload'
import type { EfficientUploadEntry, EfficientUploadOptions } from './efficient-upload'

/**
 * 客户端分发上传的 **api 接线模块**：`lib/client-dist/` 内唯一 import `@/api/*` 的模块。
 *
 * 分层约定（为可测性服务，勿让纯逻辑反向依赖 api）：
 * - 纯逻辑留在 `chunked-upload.ts`（切片数学 / 进度归并 / 取消错误）、
 *   `client-upload-plan.ts`（hash、装箱、分路、并发池）、`efficient-upload.ts`（上传编排，
 *   取数经 `EfficientUploadDeps` 注入）——它们不 import `@/api/*`，单测可直接塞替身，
 *   无需 mock 模块；
 * - 应用侧 api 实现只在**本模块**注入一次，对外仍是发布页要用的两个入口：分块上传
 *   `uploadFileChunked(channelId,file,opts)` 与批量编排 `uploadFilesEfficient(channelId,entries,opts)`。
 *
 * 拆开的意义：编排分支（秒传命中 / 聚合 / 分块 / 降级）的测试不再需要穿透一层
 * 「转出 + 注入」的壳，接线本身由 `client-upload-api.test.ts` 走真实 api 契约覆盖。
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
 * 复用后端分块协议：init（声明大小得 uploadId/chunkSize/chunkCount）→ 按 chunkSize 顺序
 * PUT 各分片原始字节（幂等）→ complete（服务端拼装喂 CAS，返回与单次上传一致的
 * {sha256,md5,size,codec}）。onProgress 报已上传/总字节；signal 支持取消（取消即 DELETE 弃单）。
 *
 * 签名对 FR-250（延迟批量上传编排）稳定：uploadFileChunked(channelId,file,{onProgress,signal})。
 *
 * 流程：init → 顺序 PUT 各分片（application/octet-stream 原始字节）→ complete。
 * 0 字节文件（.gitkeep/空配置）：init(totalSize=0) 合法、无分片，直达 complete（后端落空内容 CAS）。
 * 取消（signal.aborted）在任意阶段生效：已 init 则 best-effort DELETE 弃单后抛 AbortError。
 * codec 恒 none（本期发布不压缩，与既有 ClientPublishPage 一致）。
 */
export async function uploadFileChunked(
  channelId: string,
  file: File,
  opts: UploadFileChunkedOptions = {},
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

/**
 * 批量上传一组文件，返回 key → ClientFileResult 映射（与逐文件 uploadFileChunked 同构结果）。
 * 任一任务失败即 fail-fast 抛错（调用方保草稿可重试）；取消抛 AbortError。
 *
 * 编排逻辑在 `efficient-upload.ts`，本处只注入三个取数依赖（预查 / 聚合批 / 分块），
 * 因此对外签名仍是迁移前发布页调用的那个（channelId, entries, opts）。
 */
export async function uploadFilesEfficient(
  channelId: string,
  entries: EfficientUploadEntry[],
  opts: EfficientUploadOptions = {},
): Promise<Map<string, ClientFileResult>> {
  return runEfficientUpload(
    {
      precheck: precheckClientFiles,
      uploadBatch: uploadClientFilesBatch,
      uploadChunked: uploadFileChunked,
    },
    channelId,
    entries,
    opts,
  )
}
