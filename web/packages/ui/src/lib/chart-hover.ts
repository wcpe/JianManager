/**
 * 监控页 hover 浮窗（FR-169）的纯逻辑：给定一个目标时间戳（recharts 激活点回传），
 * 在按时间戳升序的样本行里定位最近的一行，并取出该时刻各序列值。无 React 依赖，便于 vitest 单测。
 */

/** 合并后的一行：ts + 任意序列键→值（缺测为 null）。 */
export type SampleRow = { ts: string } & Record<string, number | null | string>

/** hover 浮窗里的一项：序列键、显示名、该时刻取值（缺测/无序列为 null）。 */
export interface HoverEntry {
  key: string
  name: string
  value: number | null
}

/** hover 浮窗数据：定位到的样本时间戳 + 各序列在该时刻的值。 */
export interface HoverSnapshot {
  ts: string
  entries: HoverEntry[]
}

/**
 * 在升序行集合里二分定位时间戳最接近 targetTs 的行下标。
 * 空数组返回 -1。targetTs 落在两点之间时取时间差更小的一侧（并列取左）。
 */
export function nearestRowIndex(rows: { ts: string }[], targetTs: string): number {
  const len = rows.length
  if (len === 0) return -1
  const target = new Date(targetTs).getTime()
  if (!Number.isFinite(target)) return 0

  let lo = 0
  let hi = len - 1
  // 二分收敛到相邻两点
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1
    const t = new Date(rows[mid].ts).getTime()
    if (t === target) return mid
    if (t < target) lo = mid
    else hi = mid
  }

  const dLo = Math.abs(new Date(rows[lo].ts).getTime() - target)
  const dHi = Math.abs(new Date(rows[hi].ts).getTime() - target)
  return dLo <= dHi ? lo : hi
}

/**
 * 取某时刻各序列值快照：在 rows 里定位最近 targetTs 的行，按 series 顺序读出每条序列的值。
 * 无数据返回 null。某序列在该行缺测或不存在时其 value 为 null。
 */
export function hoverSnapshotAt(
  rows: SampleRow[],
  series: { key: string; name: string }[],
  targetTs: string,
): HoverSnapshot | null {
  const idx = nearestRowIndex(rows, targetTs)
  if (idx < 0) return null
  const row = rows[idx]
  const entries: HoverEntry[] = series.map((s) => {
    const v = row[s.key]
    return { key: s.key, name: s.name, value: typeof v === 'number' && Number.isFinite(v) ? v : null }
  })
  return { ts: String(row.ts), entries }
}
