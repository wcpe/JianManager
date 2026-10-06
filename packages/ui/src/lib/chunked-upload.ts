/** 内容寻址元数据（与后端 service.ClientFileResult 对应，FR-251）。 */
export interface ChunkUploadResult {
  sha256: string
  md5: string
  size: number
  codec: string
}

/**
 * 客户端分发大文件分块上传客户端（FR-251，增强 FR-088）。
 *
 * 复用后端分块协议：init（声明大小得 uploadId/chunkSize/chunkCount）→ 按 chunkSize 顺序
 * PUT 各分片原始字节（幂等）→ complete（服务端拼装喂 CAS，返回与单次上传一致的
 * {sha256,md5,size,codec}）。onProgress 报已上传/总字节；signal 支持取消（取消即 DELETE 弃单）。
 *
 * 签名对 FR-250（延迟批量上传编排）稳定：uploadFileChunked(channelId,file,{onProgress,signal})。
 */

/** 单个分片的字节区间（半开区间 [start,end)），由纯函数 sliceRanges 产出。 */
export interface ChunkRange {
  /** 0 基分片序号。 */
  index: number
  /** 起始字节偏移（含）。 */
  start: number
  /** 结束字节偏移（不含）。 */
  end: number
}

/** uploadFileChunked 可选项。 */
export interface UploadFileChunkedOptions {
  /** 进度回调：已上传字节数 / 总字节数。片粒度推进（每片完成后回调）。 */
  onProgress?: (uploadedBytes: number, totalBytes: number) => void
  /** 取消信号：abort 后停止上传并向服务端 DELETE 弃单。 */
  signal?: AbortSignal
  /**
   * 期望分片大小（字节）。省略则由服务端敲定（默认 8 MiB，越界自动夹取）。
   * 传入仅为建议，实际以 init 返回的 chunkSize 为准。
   */
  chunkSize?: number
  /**
   * 已知的文件原始内容 sha256（FR-346：预查阶段已算出时透传）。
   * complete 请求携带之，服务端 Ingest 强校验——传输损坏在入库前即被拒。
   */
  expectedSha256?: string
}

/**
 * 计算分片的字节区间表：ceil(totalSize/chunkSize) 片，末片为余量。
 * 纯函数（无副作用），便于单测切片数学（片数/边界/末片/空文件）。
 *
 * @param totalSize 文件总字节数（>=0）。
 * @param chunkSize 每片字节数（>0）。
 * @returns 有序分片区间数组；totalSize=0（空文件）返回空数组——零次分片循环、init 后直达 complete。
 */
export function sliceRanges(totalSize: number, chunkSize: number): ChunkRange[] {
  if (chunkSize <= 0) throw new Error('chunkSize 必须为正')
  if (totalSize < 0) throw new Error('totalSize 不能为负')
  const ranges: ChunkRange[] = []
  let index = 0
  for (let start = 0; start < totalSize; start += chunkSize) {
    const end = Math.min(start + chunkSize, totalSize)
    ranges.push({ index, start, end })
    index += 1
  }
  return ranges
}

/**
 * 归并进度：已完成 completedChunks 片（每片 chunkSize，末片除外）后的已上传字节数，
 * 上限不超过 totalSize（防末片按整片计超过总数）。纯函数，便于单测。
 */
export function progressBytes(
  completedChunks: number,
  chunkSize: number,
  totalSize: number,
): number {
  return Math.min(completedChunks * chunkSize, totalSize)
}

/** 抛出取消错误（与 DOMException AbortError 语义一致，供调用方识别「用户取消」）。 */
export function abortError(): Error {
  return new DOMException('上传已取消', 'AbortError')
}

/**
 * 分块上传一个文件到指定频道，返回内容寻址元数据（与单次上传 usePublishClientFile 同结构）。
 *
 * 流程：init → 顺序 PUT 各分片（application/octet-stream 原始字节）→ complete。
 * 0 字节文件（.gitkeep/空配置）：init(totalSize=0) 合法、无分片，直达 complete（后端落空内容 CAS）。
 * 取消（signal.aborted）在任意阶段生效：已 init 则 best-effort DELETE 弃单后抛 AbortError。
 * codec 恒 none（本期发布不压缩，与既有 ClientPublishPage 一致）。
 */