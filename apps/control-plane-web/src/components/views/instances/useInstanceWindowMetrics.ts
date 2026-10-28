/**
 * @file useInstanceWindowMetrics：可见窗口实例的实时指标批量取数（消费 FR-340 的
 *       `POST /metrics/series/batch`）。行渲染件不取数（ADR-103 b）：窗口归属虚拟表——
 *       表把 `range` 上报给页面，页面据此切片实例列表并在此 hook 统一取数，逐行注入快照。
 *
 * 取数边界（性能判据）：
 * - 单次 ≤50 目标（batch 端点硬上限，镜像后端 `metricBatchMaxTargets`；可见窗口通常远小于此，
 *   超出部分截断，滚动后自然补齐）；
 * - id 集合按 300ms 防抖才切换查询键——虚拟滚动每帧都会改窗口，不防抖会造成请求风暴；
 * - **增量取数**（本轮修复）：滚动时窗口只是平移，新进视口的目标通常只有几台。此前整窗重取，
 *   每次都是一份 1h raw 全量序列（devmock 实测单次响应 400~550KB）——滚一下就是几百 KB 白流量
 *   加几百毫秒主线程解析。现在 fetch 内部只请求**本地还没有的目标**，并把结果并入累积缓存；
 *   已取到的目标靠 30s 节拍刷新。
 * - 30s 轮询 + `placeholderData: keepPreviousData`（窗口切换期间保留上一批数据，避免整表闪空）。
 *
 * 【缓存放在哪、为什么不放组件状态】它必须是「跨多次请求累积」的，但两条常规路线都被 React
 * 规则拦住：放 state 就得在 effect 里同步 `setState`（`react-hooks/set-state-in-effect`），
 * 放 ref 就得在渲染期读它（`react-hooks/refs`）。因此把读写都收进 **queryFn**
 * （异步、非渲染期，规则允许），渲染期只读 React Query 的 `data`——
 * 累积缓存随查询结果一起被 React Query 管理，组件里没有任何额外状态。
 * @input api/metrics（useMetricSeriesBatch）、InstanceInfo、InstanceMetricSnapshot、HEARTBEAT_SLOTS
 * @output useInstanceWindowMetrics、snapshotFromSeries（导出供针对性测试）
 * @sync InstanceRowMetrics.tsx（快照契约与心跳格数同源）、InstancesPage.tsx（容器装配）
 */
import { useEffect, useMemo, useState } from 'react'
import type { MetricRange } from '@jianmanager/ui/charts/RangePicker'

import { useMetricSeriesBatch } from '@/api/metrics'
import type { MetricResolution } from '@/api/metrics'
import { HEARTBEAT_SLOTS } from '@/components/views/instances/InstanceRowMetrics'
import type { InstanceMetricSnapshot } from '@/components/views/instances/InstanceRowMetrics'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import type { MetricSeries, SeriesPoint } from '@/lib/metrics/metric-series'

/** 行内四段的指标键（与实例详情监控段同源口径）。 */
const WINDOW_METRIC_KEYS = [
  'inst_tps',
  'inst_cpu_pct',
  'inst_heap_used',
  'inst_players_online',
  // FR-511：侧栏行尾要「当前/最大」，上限必须在同一批里取，否则要二次请求。
  'inst_players_max',
]

/** batch 端点单次目标上限（镜像后端 `metricBatchMaxTargets = 50`）。 */
const MAX_TARGETS = 50

/** 刷新节拍（毫秒）：与后端采样/轮询周期一致。 */
const REFRESH_MS = 30_000

/** 缓存值跨过几个节拍后重取（1 = 每拍都刷）。 */
const STALE_TICKS = 1

/** 一条缓存：快照 + 取得时的节拍号（节拍号决定它何时算过期）。 */
interface CachedSnapshot {
  snapshot: InstanceMetricSnapshot
  tick: number
}

/**
 * 累积快照缓存（uuid → 快照 + 取得节拍），**模块级普通容器**。
 *
 * 【为什么不是 useState / useRef】
 * 它要跨多次请求累积，而两条常规路线各自踩一条 React 规则：
 * - `useState` + effect 写入 → `react-hooks/set-state-in-effect`（级联渲染）；
 * - `useRef` 并在渲染期读 → `react-hooks/refs`（渲染期读可变引用）。
 * 这里用一个**不在 React 数据模型里**的容器：渲染期读写它不构成状态更新（无需调度），
 * 也不参与 React 的依赖追踪——它的「何时变」完全由本 hook 的 `data` / `tick` 驱动重渲染。
 *
 * 代价与复查条件（必须记录，否则后来者无从判断它是否仍成立）：
 * - 生命周期跨组件卸载 → 换账号/换租户时若 uuid 碰撞会读到别人的旧值；
 *   当前 uuid 全局唯一且不含租户前缀差异，若将来引入多租户隔离，**必须**在登出时调用
 *   `resetWindowMetricsCache()`（或改为按租户分桶）。
 * - 测试间会残留 → 用 `resetWindowMetricsCache()` 在每个用例前清空。
 */
const snapshotCache = new Map<string, CachedSnapshot>()

/** 窗口取数区间与粒度：1h raw（30s 采样）——心跳带取末 40 点 ≈ 近 20 分钟。 */
const WINDOW_RANGE: MetricRange = '1h'
const WINDOW_RESOLUTION: MetricResolution = 'raw'

/**
 * 轻量模式的区间与粒度（侧栏用）：只需要**末点**（当前人数 / 上限 / TPS / CPU / 内存），
 * 不需要 1h 的 120 个采样点，更不需要心跳带（侧栏不画心跳带，悬停卡的心跳来自同一批数据，
 * 但侧栏滚一屏就是几十台——1h raw × 5 指标 × 30 台，实测响应 575KB）。
 *
 * 取 `1h + 5m` 聚合档：走预聚合表 `metric_rollup5ms`（后端 `queryPoints` 的三档选择），
 * **每个目标每指标只有 12 个桶**（1h ÷ 5m），而 1h raw 是 120 个点——小一个数量级。
 * 末点即「最近一个 5 分钟桶」，对侧栏完全够用（它本来就是 30s 刷新一次的近似值）。
 *
 * 【为什么不用 24h + 5m】桶数 = 区间 ÷ 粒度：24h ÷ 5m = 288 个桶，比 1h raw 的 120 点还多，
 * 实测响应反而从 575KB 涨到 915KB。选档位的判据是**桶数**，不是粒度名字看起来更「粗」。
 */
const LIGHT_RANGE: MetricRange = '1h'
const LIGHT_RESOLUTION: MetricResolution = '5m'

/** 末点有效值（avg 口径，与图表一致）；全为缺测时 null。 */
function lastAvg(points: SeriesPoint[]): number | null {
  for (let i = points.length - 1; i >= 0; i--) {
    const v = points[i]?.avg
    if (v != null && Number.isFinite(v)) return v
  }
  return null
}

/** 单实例序列组 → 行内快照（纯函数，导出供针对性测试）。 */
export function snapshotFromSeries(series: MetricSeries[]): InstanceMetricSnapshot {
  const byKey = new Map(series.map((s) => [s.metricKey, s]))
  const tpsPoints = byKey.get('inst_tps')?.points ?? []
  return {
    tps: lastAvg(tpsPoints),
    cpuPct: lastAvg(byKey.get('inst_cpu_pct')?.points ?? []),
    memUsedBytes: lastAvg(byKey.get('inst_heap_used')?.points ?? []),
    players: lastAvg(byKey.get('inst_players_online')?.points ?? []),
    playersMax: lastAvg(byKey.get('inst_players_max')?.points ?? []),
    // 心跳三态：缺测 null、值 >0 活跃、=0 停摆（TPS 归零即无活动）。
    heartbeat: tpsPoints.slice(-HEARTBEAT_SLOTS).map((p) => (p.avg == null ? null : p.avg > 0)),
    /*
      序列一并带出（缩略图与悬浮图用）：这些点本来就在响应里，丢掉只会逼后续再请求一次。
      抽稀到最多 SPARK_POINTS 个点——行内缩略图只有约 60px 宽，200 个点画不出更多信息，
      却要多占内存并在悬浮时重绘整条路径。
    */
    series: {
      tps: thin(tpsPoints),
      players: thin(byKey.get('inst_players_online')?.points ?? []),
      cpuPct: thin(byKey.get('inst_cpu_pct')?.points ?? []),
      memBytes: thin(byKey.get('inst_heap_used')?.points ?? []),
    },
  }
}

/** 缩略图/悬浮图的最大点数。 */
const SPARK_POINTS = 60

/**
 * 序列抽稀：等间隔取样，保留最后一点。
 *
 * 不取平均而是**取样**：平均值会把尖峰抹平，而缩略图的价值恰恰在「刚才是不是抖过」。
 * 保留末点是因为缩略图右端应当与行内当前值读数一致（否则看起来自相矛盾）。
 */
function thin(points: SeriesPoint[]): { ts: string; value: number }[] {
  const vals = points
    .filter((p): p is SeriesPoint & { avg: number } => p.avg != null)
    .map((p) => ({ ts: p.ts, value: p.avg }))
  if (vals.length <= SPARK_POINTS) return vals
  const step = vals.length / SPARK_POINTS
  const out: { ts: string; value: number }[] = []
  for (let i = 0; i < SPARK_POINTS; i++) out.push(vals[Math.floor(i * step)]!)
  out[out.length - 1] = vals[vals.length - 1]!
  return out
}

/**
 * 可见窗口实例 → uuid→快照 映射。`visible` 由页面按虚拟表上报的 range 切片而来；
 * 调用方无需稳定数组引用（内部按去重排序后的 id 集合定键）。
 */
export function useInstanceWindowMetrics(
  visible: InstanceInfo[],
  options: {
    /**
     * 轻量模式（侧栏用）：只要末点值，`range/resolution` 换成预聚合档以大幅缩小响应。
     * 代价是心跳带只有 5 分钟粒度的粗格——侧栏不展示心跳，故无影响。
     */
    light?: boolean
  } = {},
): Map<string, InstanceMetricSnapshot> {
  const light = options.light === true
  const targetIds = useMemo(() => {
    const seen = new Set<string>()
    for (const inst of visible) {
      if (inst.uuid) seen.add(inst.uuid)
      if (seen.size >= MAX_TARGETS) break
    }
    return [...seen].sort()
  }, [visible])
  const idsKey = useMemo(() => targetIds.join(','), [targetIds])

  // 防抖：滚动中窗口每帧变化，静默 300ms 后才认新窗口（期间 keepPreviousData 兜底展示）。
  const [stableKey, setStableKey] = useState(idsKey)
  useEffect(() => {
    const timer = window.setTimeout(() => setStableKey(idsKey), 300)
    return () => window.clearTimeout(timer)
  }, [idsKey])
  const stableIds = useMemo(() => (stableKey ? stableKey.split(',') : []), [stableKey])

  /**
   * 刷新节拍：每 `REFRESH_MS` 推进一次，是「同一批目标再拉一遍」的唯一表达方式
   * （查询键不变就永远命中缓存），同时作为缓存过期判定的基准。
   */
  const [tick, setTick] = useState(0)
  useEffect(() => {
    const timer = window.setInterval(() => setTick((n) => n + 1), REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [])

  /**
   * 本批真正要请求的目标 = 视口内「还没取过」或「已过期」的那些。
   *
   * 滚动是平移窗口：上一屏通常有十几台仍在下一屏，只补新进视口的那几台。
   */
  const fetchIds = useMemo(
    () =>
      stableIds
        .filter((uuid) => {
          const hit = snapshotCache.get(uuid)
          return !hit || tick - hit.tick >= STALE_TICKS
        })
        .slice(0, MAX_TARGETS),
    [stableIds, tick],
  )

  const { data } = useMetricSeriesBatch({
    scope: 'instance',
    targetIds: fetchIds,
    range: light ? LIGHT_RANGE : WINDOW_RANGE,
    metrics: WINDOW_METRIC_KEYS,
    resolution: light ? LIGHT_RESOLUTION : WINDOW_RESOLUTION,
    refreshToken: tick,
  })

  return useMemo(() => {
    // 新到的一批并入模块级缓存（渲染期纯写一个普通容器，非 state / 非 ref）。
    if (data) {
      for (const [uuid, series] of Object.entries(data.series)) {
        snapshotCache.set(uuid, { snapshot: snapshotFromSeries(series), tick })
      }
    }
    // 只返回视口内目标的快照：滑出视口的旧值不参与渲染。
    const out = new Map<string, InstanceMetricSnapshot>()
    for (const uuid of stableIds) {
      const hit = snapshotCache.get(uuid)
      if (hit) out.set(uuid, hit.snapshot)
    }
    return out
    // `data` 到场即重建；`tick`/`stableIds` 变化时视口或新鲜度变了，同样要重算。
  }, [data, stableIds, tick])
}

/** 清空累积缓存（测试用：模块级容器跨用例会残留）。 */
export function resetWindowMetricsCache(): void {
  snapshotCache.clear()
}
