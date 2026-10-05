/**
 * Bot 健康条多段着色（FR-147，兑现 FR-040 备注「细分 connecting/error」）。
 * 抽成无 React 依赖的纯函数以便单测。数据来自 summary 全局 byStatus（精确各状态计数），
 * 比旧的「在线 vs 其余」两段健康条更细：connected / connecting / error / stopped 四段。
 */

/** 健康段类型，固定渲染顺序（绿→琥珀→红→灰）。 */
export type HealthKind = 'connected' | 'connecting' | 'error' | 'stopped'

/** 渲染顺序约定。 */
const HEALTH_ORDER: HealthKind[] = ['connected', 'connecting', 'error', 'stopped']

/** 健康条的一段：占比 0~1，用于按比例渲染宽度。 */
export interface HealthBreakdownSegment {
  kind: HealthKind
  count: number
  ratio: number
}

/**
 * 把一组 Bot 的 total 与各状态计数（byStatus）拆成多段健康分布。
 *
 * 状态归并：
 * - connected → connected（在线，绿）
 * - connecting + pending → connecting（连接生命周期前段，琥珀）
 * - error → error（异常，红）
 * - stopped → stopped（已停止，灰）
 * 已知状态计数之和不足 total 时，余量兜底归入 stopped（覆盖未上报/未知状态）；
 * 之和超过 total（数据竞态）时按 total 截断，保证占比之和 ≤ 1。
 * total<=0 返回空数组（调用方渲染空轨道）。
 */
export function healthBreakdown(
  total: number,
  byStatus: Record<string, number>,
): HealthBreakdownSegment[] {
  if (total <= 0) return []

  const raw: Record<HealthKind, number> = {
    connected: byStatus.connected ?? 0,
    connecting: (byStatus.connecting ?? 0) + (byStatus.pending ?? 0),
    error: byStatus.error ?? 0,
    stopped: byStatus.stopped ?? 0,
  }

  let known = raw.connected + raw.connecting + raw.error + raw.stopped
  // 余量归入 stopped（未上报/未知状态兜底）。
  if (known < total) {
    raw.stopped += total - known
    known = total
  }

  // 数据竞态导致溢出时，从末段（stopped→error→connecting→connected）回收多余计数。
  let overflow = known - total
  if (overflow > 0) {
    for (const kind of [...HEALTH_ORDER].reverse()) {
      if (overflow <= 0) break
      const take = Math.min(raw[kind], overflow)
      raw[kind] -= take
      overflow -= take
    }
  }

  return HEALTH_ORDER.filter((kind) => raw[kind] > 0).map((kind) => ({
    kind,
    count: raw[kind],
    ratio: raw[kind] / total,
  }))
}
