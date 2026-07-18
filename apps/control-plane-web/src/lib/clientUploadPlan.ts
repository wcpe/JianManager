/**
 * 客户端分发上传增效的纯逻辑（FR-346，增强 FR-250/251）。
 *
 * 这里只放与网络/React 无关的可单测部分：大小分路、贪心装箱、列表分批、
 * 有限并发任务池、单调进度聚合与浏览器内 SHA-256。编排（预查→秒传→聚合/分块）
 * 在 efficientUpload.ts 消费这些函数。
 */

// ── 阈值常量（与服务端上限镜像，spec §3/§4）─────────────────────────────────

/** 聚合上传单文件上限（= FR-251 defaultChunkSize 8 MiB）：≤ 此值走聚合，> 此值走分块。 */
export const AGGREGATE_MAX_FILE_BYTES = 8 * 1024 * 1024
/** 预查 hash 计算上限：WebCrypto 无流式、整读进内存，> 此值直接走分块不预查。 */
export const HASH_MAX_FILE_BYTES = 256 * 1024 * 1024
/** 单次预查请求最多携带的 hash 数（镜像服务端 precheckMaxEntries）。 */
export const PRECHECK_MAX_ENTRIES = 500
/** 单聚合批最多文件数（镜像服务端 batchMaxFiles）。 */
export const BATCH_MAX_FILES = 200
/** 单聚合批总字节上限（镜像服务端 batchMaxTotalBytes）。 */
export const BATCH_MAX_TOTAL_BYTES = 32 * 1024 * 1024
/** 上传任务池并发上限（spec 定 3~5 区间取 4；聚合批与分块文件均计一个任务）。 */
export const UPLOAD_CONCURRENCY = 4

// ── 分路 ────────────────────────────────────────────────────────────────

/** 是否为该文件计算 hash 并参与秒传预查（≤ 256 MiB 才算，护内存）。 */
export function shouldHash(size: number): boolean {
  return size <= HASH_MAX_FILE_BYTES
}

/** 未命中秒传时的上传路径：小文件聚合 / 大文件 FR-251 分块。 */
export function uploadRouteFor(size: number): 'aggregate' | 'chunked' {
  return size <= AGGREGATE_MAX_FILE_BYTES ? 'aggregate' : 'chunked'
}

// ── 装箱 / 分批 ──────────────────────────────────────────────────────────

/**
 * 贪心装箱：按输入顺序把条目装入聚合批，任一批不超过 maxFiles 个且总字节不超 maxTotal。
 * 前提：单条目 size ≤ maxTotal（聚合路径单文件 ≤ 8 MiB < 32 MiB 恒成立）；
 * 保序（批内与批间均保持输入顺序），便于断言与结果对齐。纯函数。
 */
export function packBatches<T>(
  items: T[],
  sizeOf: (item: T) => number,
  maxFiles: number = BATCH_MAX_FILES,
  maxTotal: number = BATCH_MAX_TOTAL_BYTES,
): T[][] {
  const batches: T[][] = []
  let current: T[] = []
  let currentBytes = 0
  for (const item of items) {
    const size = sizeOf(item)
    if (current.length > 0 && (current.length >= maxFiles || currentBytes + size > maxTotal)) {
      batches.push(current)
      current = []
      currentBytes = 0
    }
    current.push(item)
    currentBytes += size
  }
  if (current.length > 0) batches.push(current)
  return batches
}

/** 按固定大小切分列表（预查请求分批用）。size<=0 抛错。纯函数。 */
export function chunkList<T>(items: T[], size: number): T[][] {
  if (size <= 0) throw new Error('size 必须为正')
  const out: T[][] = []
  for (let i = 0; i < items.length; i += size) {
    out.push(items.slice(i, i + size))
  }
  return out
}

// ── 有限并发任务池 ────────────────────────────────────────────────────────

/**
 * 以 limit 并发运行任务，返回与 tasks 同序的结果。
 *
 * fail-fast：任一任务失败后不再启动新任务，等在途任务落定后以**首个**错误拒绝
 * （在途任务的后续失败不覆盖首错）。signal 中止时同样停止派发并抛出 AbortError 语义
 * 的错误（由任务自身抛出的取消错误优先）。
 */
export async function runLimited<T>(
  tasks: Array<() => Promise<T>>,
  limit: number,
  signal?: AbortSignal,
): Promise<T[]> {
  if (limit <= 0) throw new Error('limit 必须为正')
  const results = new Array<T>(tasks.length)
  let next = 0
  let firstError: unknown = null

  async function worker(): Promise<void> {
    for (;;) {
      if (firstError !== null || signal?.aborted) return
      const i = next
      if (i >= tasks.length) return
      next += 1
      try {
        results[i] = await tasks[i]()
      } catch (err) {
        if (firstError === null) firstError = err
        return
      }
    }
  }

  const workers = Array.from({ length: Math.min(limit, tasks.length) }, () => worker())
  await Promise.all(workers)
  if (firstError !== null) throw firstError
  if (signal?.aborted && next < tasks.length) {
    throw new DOMException('上传已取消', 'AbortError')
  }
  return results
}

// ── 进度聚合（单调、不 NaN）─────────────────────────────────────────────────

/** 单调进度聚合器：已完成字节 + 各在途任务字节，汇报值只增不减、封顶 totalBytes。 */
export interface ProgressTracker {
  /** 覆盖式更新某任务的在途已传字节（非法值忽略，超任务总量由调用方自行封顶）。 */
  setInflight(taskId: string, bytes: number): void
  /** 任务完成：其总字节并入完成累计并清在途。 */
  complete(taskId: string, bytes: number): void
  /** 当前聚合已传字节（经单调 clamp 与 totalBytes 封顶）。 */
  current(): number
}

/**
 * 创建进度聚合器。并发任务各自回报在途字节（axios onUploadProgress / 分块片回调），
 * 聚合值 = min(total, 完成累计 + Σ在途)，再经 max(上次汇报, 本次) 单调 clamp——
 * 重试回退、multipart 开销折算等都不会让总进度倒退；NaN/负值直接忽略。
 */
export function createProgressTracker(
  totalBytes: number,
  onChange?: (uploadedBytes: number) => void,
): ProgressTracker {
  const inflight = new Map<string, number>()
  let completed = 0
  let reported = 0

  const recompute = () => {
    let sum = completed
    for (const v of inflight.values()) sum += v
    const next = Math.min(Math.max(sum, 0), Math.max(totalBytes, 0))
    if (next > reported) {
      reported = next
      onChange?.(reported)
    }
  }

  return {
    setInflight(taskId, bytes) {
      if (!Number.isFinite(bytes) || bytes < 0) return
      inflight.set(taskId, bytes)
      recompute()
    },
    complete(taskId, bytes) {
      inflight.delete(taskId)
      if (Number.isFinite(bytes) && bytes > 0) completed += bytes
      recompute()
    },
    current() {
      return reported
    },
  }
}

// ── 浏览器内容 hash ─────────────────────────────────────────────────────────

const SHA256_INITIAL = [
  0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
  0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
]

const SHA256_ROUND = [
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]

function rotateRight(value: number, bits: number): number {
  return (value >>> bits) | (value << (32 - bits))
}

/** 补齐 SHA-256 消息并写入 64 位大端 bit 长度。 */
function padSha256Input(input: Uint8Array): Uint8Array {
  const paddedLength = Math.ceil((input.length + 9) / 64) * 64
  const padded = new Uint8Array(paddedLength)
  padded.set(input)
  padded[input.length] = 0x80
  const bitLength = input.length * 8
  const view = new DataView(padded.buffer)
  view.setUint32(paddedLength - 8, Math.floor(bitLength / 0x100000000), false)
  view.setUint32(paddedLength - 4, bitLength >>> 0, false)
  return padded
}

/** 从一个 64 字节块生成 64 轮消息调度表。 */
function fillSha256Schedule(input: Uint8Array, offset: number, schedule: Uint32Array): void {
  for (let i = 0; i < 16; i += 1) {
    const p = offset + i * 4
    schedule[i] = ((input[p] << 24) | (input[p + 1] << 16) | (input[p + 2] << 8) | input[p + 3]) >>> 0
  }
  for (let i = 16; i < 64; i += 1) {
    const x = schedule[i - 15]
    const y = schedule[i - 2]
    const s0 = rotateRight(x, 7) ^ rotateRight(x, 18) ^ (x >>> 3)
    const s1 = rotateRight(y, 17) ^ rotateRight(y, 19) ^ (y >>> 10)
    schedule[i] = (schedule[i - 16] + s0 + schedule[i - 7] + s1) >>> 0
  }
}

/** 执行一个 SHA-256 压缩块。 */
function compressSha256Block(state: Uint32Array, schedule: Uint32Array): void {
  let [a, b, c, d, e, f, g, h] = state
  for (let i = 0; i < 64; i += 1) {
    const sum1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25)
    const choice = (e & f) ^ (~e & g)
    const temp1 = (h + sum1 + choice + SHA256_ROUND[i] + schedule[i]) >>> 0
    const sum0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22)
    const majority = (a & b) ^ (a & c) ^ (b & c)
    const temp2 = (sum0 + majority) >>> 0
    h = g
    g = f
    f = e
    e = (d + temp1) >>> 0
    d = c
    c = b
    b = a
    a = (temp1 + temp2) >>> 0
  }
  const work = [a, b, c, d, e, f, g, h]
  for (let i = 0; i < state.length; i += 1) state[i] = (state[i] + work[i]) >>> 0
}

/** HTTP 非安全上下文的无依赖 SHA-256 兜底，仅由小文件聚合路径使用。 */
function sha256HexFallback(input: Uint8Array): string {
  const padded = padSha256Input(input)
  const state = Uint32Array.from(SHA256_INITIAL)
  const schedule = new Uint32Array(64)
  for (let offset = 0; offset < padded.length; offset += 64) {
    fillSha256Schedule(padded, offset, schedule)
    compressSha256Block(state, schedule)
  }
  return Array.from(state, (word) => word.toString(16).padStart(8, '0')).join('')
}

function hasNativeSha256(): boolean {
  return typeof crypto !== 'undefined' && crypto.subtle !== undefined
}

/**
 * 计算 Blob 原始内容的 SHA-256（十六进制小写）= 秒传预查查重键（spec §2）。
 * 安全上下文优先 WebCrypto；普通 HTTP 对小文件使用纯 JS 兜底，避免增效链路阻断发布。
 */
export async function sha256HexOfBlob(blob: Blob): Promise<string> {
  const bytes = new Uint8Array(await blob.arrayBuffer())
  if (!hasNativeSha256()) return sha256HexFallback(bytes)
  const digest = await crypto.subtle.digest('SHA-256', bytes)
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, '0')).join('')
}
