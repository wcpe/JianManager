import {
  precheckClientFiles,
  uploadClientFilesBatch,
  type BatchUploadEntry,
  type ClientFileResult,
  type PrecheckFileEntry,
} from '@/api/clientVersions'
import { uploadFileChunked } from './chunkedUpload'
import {
  PRECHECK_MAX_ENTRIES,
  UPLOAD_CONCURRENCY,
  chunkList,
  createProgressTracker,
  packBatches,
  runLimited,
  sha256HexOfBlob,
  shouldHash,
  uploadRouteFor,
} from './clientUploadPlan'

/**
 * 客户端分发批量上传编排器（FR-346，增强 FR-250/251）。
 *
 * 流水线：串行 hash（≤256MiB 者）→ 分批秒传预查（失败降级全 miss，不阻断发布）→
 * 命中者零字节直取结果；miss 小文件（≤8MiB）贪心装箱走聚合端点；其余走 FR-251 分块
 * （有 hash 者 complete 顺带 expectedSha256）→ 聚合批与分块文件混入并发 4 任务池。
 * 进度经单调聚合器汇报（不倒退、不 NaN）；signal 取消贯穿各阶段（分块自带弃单）。
 */

/** 一个待上传单元：调用方的复用键（FR-250 dedup key）+ 内容与展示名。 */
export interface EfficientUploadEntry {
  /** 结果映射键（同键复用同一结果；调用方保证入参键唯一）。 */
  key: string
  /** 浏览器内文件（内容源）。 */
  file: File
  /** 展示名（进度条当前项，一般为目标相对路径）。 */
  label: string
}

/** 编排进度快照（页面据此渲染；本模块不做 i18n）。 */
export interface EfficientUploadProgress {
  /** 阶段：hashing=本地校验计算；uploading=字节上传（含秒传落定）。 */
  phase: 'hashing' | 'uploading'
  /** 已完成 hash 的文件数 / 需 hash 的文件数。 */
  hashedFiles: number
  totalFilesToHash: number
  /** 已上传字节（单调）/ 总字节。 */
  uploadedBytes: number
  totalBytes: number
  /** 已落定文件数（含秒传命中）/ 总文件数。 */
  completedFiles: number
  totalFiles: number
  /** 秒传命中文件数。 */
  reusedFiles: number
  /** 当前活跃任务（并发下取最近启动者）；batch 为聚合批（count=批内文件数）。 */
  current: { kind: 'file'; name: string } | { kind: 'batch'; count: number } | null
}

/** uploadFilesEfficient 可选项。 */
export interface EfficientUploadOptions {
  /** 取消信号：中止 hash/预查/上传（分块路径自带服务端弃单）。 */
  signal?: AbortSignal
  /** 进度回调（状态推进即回调；uploadedBytes 已经单调 clamp）。 */
  onProgress?: (p: EfficientUploadProgress) => void
}

/** 抛出与 DOMException AbortError 语义一致的取消错误。 */
function abortError(): Error {
  return new DOMException('上传已取消', 'AbortError')
}

/**
 * 批量上传一组文件，返回 key → ClientFileResult 映射（与逐文件 uploadFileChunked 同构结果）。
 * 任一任务失败即 fail-fast 抛错（调用方保草稿可重试）；取消抛 AbortError。
 */
export async function uploadFilesEfficient(
  channelId: string,
  entries: EfficientUploadEntry[],
  opts: EfficientUploadOptions = {},
): Promise<Map<string, ClientFileResult>> {
  const { signal, onProgress } = opts
  const results = new Map<string, ClientFileResult>()
  if (entries.length === 0) return results
  if (signal?.aborted) throw abortError()

  const totalBytes = entries.reduce((s, e) => s + e.file.size, 0)
  const toHash = entries.filter((e) => shouldHash(e.file.size))

  // ── 进度状态（经 emit 汇总为快照）──────────────────────────────────────
  let phase: EfficientUploadProgress['phase'] = 'hashing'
  let hashedFiles = 0
  let completedFiles = 0
  let reusedFiles = 0
  let uploadedBytes = 0
  let current: EfficientUploadProgress['current'] = null
  const emit = () =>
    onProgress?.({
      phase,
      hashedFiles,
      totalFilesToHash: toHash.length,
      uploadedBytes,
      totalBytes,
      completedFiles,
      totalFiles: entries.length,
      reusedFiles,
      current,
    })
  const tracker = createProgressTracker(totalBytes, (n) => {
    uploadedBytes = n
    emit()
  })

  // ── 1) hash 阶段：串行整读计算（内存峰值 ≤ 单文件；>256MiB 者跳过）────────
  const hashByKey = new Map<string, string>()
  emit()
  for (const e of toHash) {
    if (signal?.aborted) throw abortError()
    current = { kind: 'file', name: e.label }
    hashByKey.set(e.key, await sha256HexOfBlob(e.file))
    hashedFiles += 1
    emit()
  }

  // ── 2) 预查阶段：分批 ≤500 查询；任一批失败即整体降级为全 miss（纯优化不阻断）──
  phase = 'uploading'
  current = null
  emit()
  const hitByKey = new Map<string, ClientFileResult>()
  const hashedEntries = toHash.filter((e) => hashByKey.has(e.key))
  try {
    for (const group of chunkList(hashedEntries, PRECHECK_MAX_ENTRIES)) {
      if (signal?.aborted) throw abortError()
      const query: PrecheckFileEntry[] = group.map((e) => ({
        sha256: hashByKey.get(e.key)!,
        size: e.file.size,
      }))
      const res = await precheckClientFiles(channelId, query, { signal })
      res.forEach((r, i) => {
        if (r.hit && r.result) hitByKey.set(group[i].key, r.result)
      })
    }
  } catch (err) {
    // 取消要透传；其余预查失败降级为全部未命中（spec §4：优化失效 ≠ 功能失效）。
    if (signal?.aborted || (err instanceof DOMException && err.name === 'AbortError')) throw err
    hitByKey.clear()
  }

  // 命中者：零字节落定（字节即刻计满、结果直取）。
  for (const e of entries) {
    const hit = hitByKey.get(e.key)
    if (!hit) continue
    results.set(e.key, hit)
    reusedFiles += 1
    completedFiles += 1
    tracker.complete(`hit:${e.key}`, e.file.size)
  }
  emit()

  // ── 3) 装箱与任务构建 ─────────────────────────────────────────────────
  const misses = entries.filter((e) => !results.has(e.key))
  const aggregate = misses.filter(
    (e) => hashByKey.has(e.key) && uploadRouteFor(e.file.size) === 'aggregate',
  )
  const chunked = misses.filter((e) => !aggregate.includes(e))

  const tasks: Array<() => Promise<void>> = []

  for (const [bi, batch] of packBatches(aggregate, (e) => e.file.size).entries()) {
    const batchBytes = batch.reduce((s, e) => s + e.file.size, 0)
    const taskId = `batch:${bi}`
    tasks.push(async () => {
      current = { kind: 'batch', count: batch.length }
      emit()
      const batchEntries: BatchUploadEntry[] = batch.map((e) => ({
        filename: e.file.name,
        size: e.file.size,
        sha256: hashByKey.get(e.key)!,
        file: e.file,
      }))
      const out = await uploadClientFilesBatch(channelId, batchEntries, {
        signal,
        // multipart 报量含协议开销，按批总字节封顶折算。
        onUploadProgress: (loaded) => tracker.setInflight(taskId, Math.min(loaded, batchBytes)),
      })
      out.forEach((r, i) => results.set(batch[i].key, r))
      completedFiles += batch.length
      tracker.complete(taskId, batchBytes)
    })
  }

  for (const e of chunked) {
    tasks.push(async () => {
      current = { kind: 'file', name: e.label }
      emit()
      const res = await uploadFileChunked(channelId, e.file, {
        signal,
        expectedSha256: hashByKey.get(e.key),
        onProgress: (fileUploaded) =>
          tracker.setInflight(e.key, Math.min(fileUploaded, e.file.size)),
      })
      results.set(e.key, res)
      completedFiles += 1
      tracker.complete(e.key, e.file.size)
    })
  }

  // ── 4) 并发执行（fail-fast；取消由各请求的 signal 与池共同生效）──────────
  await runLimited(tasks, UPLOAD_CONCURRENCY, signal)
  if (signal?.aborted) throw abortError()
  current = null
  emit()
  return results
}
