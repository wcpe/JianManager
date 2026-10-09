/**
 * 分块上传的**纯逻辑**与类型契约（FR-251，增强 FR-088）：切片数学、进度归并、取消错误。
 *
 * 不 import `@/api/*`——网络侧（init → 顺序 PUT 分片 → complete → 失败弃单）在
 * `client-upload-api.ts`，api 实现只在那边注入。拆开的收益是切片数学与进度归并
 * 能被单测直接覆盖，不必先 mock 掉 axios。
 */

/** 内容寻址元数据（与后端 service.ClientFileResult 对应，FR-251）。 */
export interface ChunkUploadResult {
  sha256: string
  md5: string
  size: number
  codec: string
}

/** 单个分片的字节区间（半开区间 [start,end)），由纯函数 sliceRanges 产出。 */
export interface ChunkRange {
  /** 0 基分片序号。 */
  index: number
  /** 起始字节偏移（含）。 */
  start: number
  /** 结束字节偏移（不含）。 */
  end: number
}

/** `uploadFileChunked`（实现在 client-upload-api.ts）可选项。 */
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
