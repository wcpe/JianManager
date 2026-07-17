import { describe, it, expect } from 'vitest'
import {
  AGGREGATE_MAX_FILE_BYTES,
  HASH_MAX_FILE_BYTES,
  BATCH_MAX_FILES,
  BATCH_MAX_TOTAL_BYTES,
  shouldHash,
  uploadRouteFor,
  packBatches,
  chunkList,
  runLimited,
  createProgressTracker,
  sha256HexOfBlob,
} from './clientUploadPlan'

/** FR-346 上传增效纯逻辑：分路 / 装箱 / 并发池 / 单调进度 / 浏览器 hash。 */

describe('分路（shouldHash / uploadRouteFor）', () => {
  it('≤8MiB 走聚合、>8MiB 走分块（边界含 8MiB 本身）', () => {
    expect(uploadRouteFor(0)).toBe('aggregate')
    expect(uploadRouteFor(AGGREGATE_MAX_FILE_BYTES)).toBe('aggregate')
    expect(uploadRouteFor(AGGREGATE_MAX_FILE_BYTES + 1)).toBe('chunked')
  })

  it('≤256MiB 才算 hash 参与预查', () => {
    expect(shouldHash(HASH_MAX_FILE_BYTES)).toBe(true)
    expect(shouldHash(HASH_MAX_FILE_BYTES + 1)).toBe(false)
    expect(shouldHash(0)).toBe(true)
  })
})

describe('packBatches 贪心装箱', () => {
  const item = (size: number) => ({ size })

  it('数量上限：201 个 1 字节文件 → 200 + 1 两批（保序）', () => {
    const items = Array.from({ length: BATCH_MAX_FILES + 1 }, (_, i) => ({ size: 1, i }))
    const batches = packBatches(items, (x) => x.size)
    expect(batches).toHaveLength(2)
    expect(batches[0]).toHaveLength(BATCH_MAX_FILES)
    expect(batches[1]).toHaveLength(1)
    // 保序：扁平化后与输入一致。
    expect(batches.flat().map((x) => x.i)).toEqual(items.map((x) => x.i))
  })

  it('字节上限：5 个 8MiB → 32MiB 封顶分两批（4+1）', () => {
    const items = Array.from({ length: 5 }, () => item(AGGREGATE_MAX_FILE_BYTES))
    const batches = packBatches(items, (x) => x.size)
    expect(batches).toHaveLength(2)
    expect(batches[0]).toHaveLength(4)
    expect(batches[0].reduce((s, x) => s + x.size, 0)).toBe(BATCH_MAX_TOTAL_BYTES)
    expect(batches[1]).toHaveLength(1)
  })

  it('空输入 → 零批；单条目 → 单批', () => {
    expect(packBatches([], () => 0)).toEqual([])
    expect(packBatches([item(3)], (x) => x.size)).toHaveLength(1)
  })

  it('每批都不超过双上限（随机体积模糊验证）', () => {
    const items = Array.from({ length: 500 }, (_, i) => item(((i * 7919) % AGGREGATE_MAX_FILE_BYTES) + 1))
    const batches = packBatches(items, (x) => x.size)
    for (const b of batches) {
      expect(b.length).toBeLessThanOrEqual(BATCH_MAX_FILES)
      expect(b.reduce((s, x) => s + x.size, 0)).toBeLessThanOrEqual(BATCH_MAX_TOTAL_BYTES)
    }
    expect(batches.flat()).toHaveLength(items.length)
  })
})

describe('chunkList', () => {
  it('按固定大小切分（余量成尾批）', () => {
    expect(chunkList([1, 2, 3, 4, 5], 2)).toEqual([[1, 2], [3, 4], [5]])
    expect(chunkList([], 3)).toEqual([])
  })
  it('非法 size 抛错', () => {
    expect(() => chunkList([1], 0)).toThrow()
  })
})

describe('runLimited 有限并发任务池', () => {
  it('并发峰值不超过 limit 且结果保序', async () => {
    let active = 0
    let peak = 0
    const tasks = Array.from({ length: 10 }, (_, i) => async () => {
      active += 1
      peak = Math.max(peak, active)
      await new Promise((r) => setTimeout(r, 5))
      active -= 1
      return i * 2
    })
    const out = await runLimited(tasks, 4)
    expect(peak).toBeLessThanOrEqual(4)
    expect(out).toEqual(tasks.map((_, i) => i * 2))
  })

  it('fail-fast：首错后不再启动新任务，以首错拒绝', async () => {
    const started: number[] = []
    const boom = new Error('boom')
    const tasks = Array.from({ length: 8 }, (_, i) => async () => {
      started.push(i)
      await new Promise((r) => setTimeout(r, 2))
      if (i === 1) throw boom
      return i
    })
    await expect(runLimited(tasks, 2)).rejects.toBe(boom)
    // 失败落定后不再派发剩余任务（最多再多启动在途 worker 拉起的下一个）。
    expect(started.length).toBeLessThan(8)
  })

  it('signal 已中止时不再派发并抛 AbortError', async () => {
    const ac = new AbortController()
    ac.abort()
    const ran: number[] = []
    const tasks = [async () => ran.push(0), async () => ran.push(1)]
    await expect(runLimited(tasks, 2, ac.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(ran).toHaveLength(0)
  })

  it('空任务表 → 空结果', async () => {
    expect(await runLimited([], 3)).toEqual([])
  })
})

describe('createProgressTracker 单调进度聚合', () => {
  it('完成累计 + 在途求和，汇报单调不倒退', () => {
    const seen: number[] = []
    const tr = createProgressTracker(100, (n) => seen.push(n))
    tr.setInflight('a', 10)
    tr.setInflight('b', 20)
    expect(tr.current()).toBe(30)
    // 在途回退（如重试）：聚合原始值下降但汇报不回退。
    tr.setInflight('b', 5)
    expect(tr.current()).toBe(30)
    tr.complete('a', 40) // 完成量可大于在途报量（按任务总字节入账）
    expect(tr.current()).toBe(45)
    // 全程单调。
    for (let i = 1; i < seen.length; i++) expect(seen[i]).toBeGreaterThanOrEqual(seen[i - 1])
  })

  it('封顶 totalBytes、忽略 NaN 与负值', () => {
    const tr = createProgressTracker(50)
    tr.setInflight('x', Number.NaN)
    tr.setInflight('x', -5)
    expect(tr.current()).toBe(0)
    tr.complete('x', 999)
    expect(tr.current()).toBe(50)
  })

  it('totalBytes=0 时恒为 0（不产生 NaN 百分比输入）', () => {
    const tr = createProgressTracker(0)
    tr.setInflight('a', 10)
    tr.complete('a', 10)
    expect(tr.current()).toBe(0)
  })
})

describe('sha256HexOfBlob', () => {
  it('已知向量：sha256("abc")', async () => {
    const hex = await sha256HexOfBlob(new Blob(['abc']))
    expect(hex).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
  })

  it('空内容：sha256("")', async () => {
    const hex = await sha256HexOfBlob(new Blob([]))
    expect(hex).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
  })
})

describe('常量与服务端上限镜像（防漂移备忘）', () => {
  it('阈值取值与 spec §3/§4 一致', () => {
    expect(AGGREGATE_MAX_FILE_BYTES).toBe(8 * 1024 * 1024)
    expect(BATCH_MAX_FILES).toBe(200)
    expect(BATCH_MAX_TOTAL_BYTES).toBe(32 * 1024 * 1024)
    expect(HASH_MAX_FILE_BYTES).toBe(256 * 1024 * 1024)
  })
})
