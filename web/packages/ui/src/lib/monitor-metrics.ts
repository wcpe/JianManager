/**
 * 监控页（FR-169）6 指标定义表 + 序列装配纯逻辑。把「哪张图画哪些 metricKey、用什么单位/格式」
 * 收敛成与 React 无关的数据，便于 vitest 单测，也让平台/节点/实例三 target 共用同一骨架。
 *
 * 仅消费现有 FR-060/061 指标键（见 internal/controlplane/model/metric.go），不新增后端字段。
 * 数据里没有的维度（如 load5/load15、磁盘读写速率、进程 TOP10）一律不画、不留占位（按 FR-169 决定）。
 */

/** 一条原始序列（来自 /metrics/series 或 overview.trends）。 */
export interface RawSeries {
  metricKey: string
  world?: string
  points: { ts: string; value: number | null }[]
}

/** 图表里的一条曲线。 */
export interface PlotSeries {
  key: string
  name: string
  /** 线色，默认按序取 --chart-1..5（与 TimeSeriesChart 一致）。 */
  color?: string
  points: { ts: string; value: number | null }[]
}

/** 数值格式化器标识：渲染层据此取对应函数（保持本模块纯数据可测）。 */
export type ValueFormat = 'pct' | 'bytes' | 'bytesPerSec' | 'load' | 'count' | 'tps' | 'ms'

/** 一张监控图的定义。 */
export interface MetricChartDef {
  /** 稳定 id（也用作 i18n key 后缀与 React key）。 */
  id: string
  /** 标题 i18n key。 */
  titleKey: string
  /** 数值格式标识。 */
  format: ValueFormat
  /** Y 轴范围；占比类传 [0,100] 更直观，其余自适应。 */
  yDomain?: [number, number]
  /**
   * 该图的序列来源。每项 = 一条曲线的 metricKey + 名称 i18n key。
   * 占比派生（mem%/disk%）通过 derive 字段表达，渲染层据此用 used/total 两条原始序列算百分比。
   */
  sources: { metricKey: string; nameKey: string }[]
  /** 派生占比：用 numerator/denominator 两个 metricKey 逐点算 numerator/denominator*100。 */
  derive?: { kind: 'ratioPct'; numerator: string; denominator: string; nameKey: string }
}

/** 字节 → G/M/K（与 OverviewPage/MetricsSegment 现有口径一致）。 */
export function formatBytes(b: number): string {
  if (!Number.isFinite(b) || b <= 0) return '0'
  if (b >= 1e9) return `${(b / 1024 / 1024 / 1024).toFixed(1)}G`
  if (b >= 1e6) return `${(b / 1024 / 1024).toFixed(0)}M`
  return `${(b / 1024).toFixed(0)}K`
}

/** 据格式标识取数值格式化函数。 */
export function formatterFor(fmt: ValueFormat): (v: number) => string {
  switch (fmt) {
    case 'pct':
      return (v) => `${v.toFixed(0)}%`
    case 'bytes':
      return formatBytes
    case 'bytesPerSec':
      return (v) => `${formatBytes(v)}/s`
    case 'load':
      return (v) => v.toFixed(2)
    case 'ms':
      return (v) => `${v.toFixed(1)}ms`
    case 'tps':
      return (v) => v.toFixed(1)
    case 'count':
    default:
      return (v) => v.toFixed(0)
  }
}

/**
 * 平台/节点监控的 6 指标定义（design §4.2）。
 * 受限于现有指标：负载只有 1 分钟（node_load），磁盘/网络 IO 用现有占用/速率指标，
 * 「资源使用率」= CPU% + 内存% + 磁盘%（占比同图对比）。
 */
export const NODE_CHART_DEFS: MetricChartDef[] = [
  {
    id: 'resource',
    titleKey: 'monitor.chart.resource',
    format: 'pct',
    yDomain: [0, 100],
    sources: [{ metricKey: 'node_cpu_pct', nameKey: 'monitor.metric.cpu' }],
    // 内存/磁盘占比由 used/total 派生（见 buildNodeChartSeries）。
  },
  {
    id: 'load',
    titleKey: 'monitor.chart.load',
    format: 'load',
    sources: [{ metricKey: 'node_load', nameKey: 'monitor.metric.load1' }],
  },
  {
    id: 'cpu',
    titleKey: 'monitor.chart.cpu',
    format: 'pct',
    yDomain: [0, 100],
    sources: [{ metricKey: 'node_cpu_pct', nameKey: 'monitor.metric.cpu' }],
  },
  {
    id: 'memory',
    titleKey: 'monitor.chart.memory',
    format: 'bytes',
    sources: [
      { metricKey: 'node_mem_used', nameKey: 'monitor.metric.memUsed' },
      { metricKey: 'node_mem_total', nameKey: 'monitor.metric.memTotal' },
    ],
  },
  {
    id: 'disk',
    titleKey: 'monitor.chart.disk',
    format: 'bytes',
    sources: [
      { metricKey: 'node_disk_used', nameKey: 'monitor.metric.diskUsed' },
      { metricKey: 'node_disk_total', nameKey: 'monitor.metric.diskTotal' },
    ],
  },
  {
    id: 'network',
    titleKey: 'monitor.chart.network',
    format: 'bytesPerSec',
    sources: [
      { metricKey: 'node_net_rx_rate', nameKey: 'monitor.metric.netRx' },
      { metricKey: 'node_net_tx_rate', nameKey: 'monitor.metric.netTx' },
    ],
  },
]

/**
 * 平台（集群聚合）监控的指标定义。受限于 /metrics/overview 仅聚合 4 条跨节点曲线
 * （node_cpu_pct 均值 / node_load 均值 / node_mem_used 合计 / inst_players_online 合计），
 * 平台视图只画这 4 张图——没有的维度（内存占比/磁盘/网络 IO）不画、不留占位（FR-169）。
 * 这是「同一骨架、三 target 可切」中平台 target 的 def 子集，与节点/实例共用 MonitorSkeleton。
 */
export const PLATFORM_CHART_DEFS: MetricChartDef[] = [
  {
    id: 'cpu',
    titleKey: 'monitor.chart.cpu',
    format: 'pct',
    yDomain: [0, 100],
    sources: [{ metricKey: 'node_cpu_pct', nameKey: 'monitor.metric.cpu' }],
  },
  {
    id: 'load',
    titleKey: 'monitor.chart.load',
    format: 'load',
    sources: [{ metricKey: 'node_load', nameKey: 'monitor.metric.load1' }],
  },
  {
    id: 'memory',
    titleKey: 'monitor.chart.memory',
    format: 'bytes',
    sources: [{ metricKey: 'node_mem_used', nameKey: 'monitor.metric.memUsed' }],
  },
  {
    id: 'players',
    titleKey: 'monitor.chart.players',
    format: 'count',
    sources: [{ metricKey: 'inst_players_online', nameKey: 'monitor.metric.players' }],
  },
]

/** 实例监控的指标定义（TPS/MSPT/堆/线程/玩家/区块——均现有 FR-060 指标）。 */
export const INSTANCE_CHART_DEFS: MetricChartDef[] = [
  { id: 'tps', titleKey: 'monitor.chart.tps', format: 'tps', sources: [{ metricKey: 'inst_tps', nameKey: 'monitor.metric.tps' }] },
  { id: 'mspt', titleKey: 'monitor.chart.mspt', format: 'ms', sources: [{ metricKey: 'inst_mspt', nameKey: 'monitor.metric.mspt' }] },
  {
    id: 'heap',
    titleKey: 'monitor.chart.heap',
    format: 'bytes',
    sources: [
      { metricKey: 'inst_heap_used', nameKey: 'monitor.metric.heapUsed' },
      { metricKey: 'inst_heap_max', nameKey: 'monitor.metric.heapMax' },
    ],
  },
  { id: 'threads', titleKey: 'monitor.chart.threads', format: 'count', sources: [{ metricKey: 'inst_threads', nameKey: 'monitor.metric.threads' }] },
  { id: 'players', titleKey: 'monitor.chart.players', format: 'count', sources: [{ metricKey: 'inst_players_online', nameKey: 'monitor.metric.players' }] },
  // 区块为分世界序列（world!=''），由渲染层按 world 拆线（buildWorldSeries）。
  { id: 'chunks', titleKey: 'monitor.chart.chunks', format: 'count', sources: [{ metricKey: 'world_loaded_chunks', nameKey: 'monitor.metric.chunks' }] },
]

/** 逐点把两条序列做 numerator/denominator*100（缺测或分母≤0 处为 null）。按时间戳对齐。 */
export function ratioPctSeries(
  numerator: { ts: string; value: number | null }[],
  denominator: { ts: string; value: number | null }[],
): { ts: string; value: number | null }[] {
  const denByTs = new Map<string, number | null>()
  for (const p of denominator) denByTs.set(p.ts, p.value)
  return numerator.map((p) => {
    const d = denByTs.get(p.ts)
    if (p.value == null || d == null || d <= 0) return { ts: p.ts, value: null }
    return { ts: p.ts, value: (p.value / d) * 100 }
  })
}

/**
 * 取一条原始序列的点（按 metricKey + 可选 world 匹配）。无匹配返回空数组。
 */
function pointsOf(raw: RawSeries[], metricKey: string, world = ''): { ts: string; value: number | null }[] {
  const s = raw.find((x) => x.metricKey === metricKey && (x.world ?? '') === world)
  return s ? s.points : []
}

/**
 * 装配一张图的曲线集合。
 * - 「资源使用率」图特判：CPU% + 内存%（mem_used/mem_total）+ 磁盘%（disk_used/disk_total）三线对比。
 * - 区块图特判：按 world 拆成多条线；worldFilter 非空时只画该世界（FR-221 下钻到世界）。
 * - 其余按 def.sources 直接取序列。
 * nameOf 为 i18n 翻译函数（注入以保持本模块纯逻辑、不依赖 i18next）。
 */
export function buildChartSeries(
  def: MetricChartDef,
  raw: RawSeries[],
  nameOf: (key: string) => string,
  worldFilter?: string,
): PlotSeries[] {
  if (def.id === 'resource') {
    const out: PlotSeries[] = []
    const cpu = pointsOf(raw, 'node_cpu_pct')
    if (cpu.length) out.push({ key: 'node_cpu_pct', name: nameOf('monitor.metric.cpu'), points: cpu })
    const mem = ratioPctSeries(pointsOf(raw, 'node_mem_used'), pointsOf(raw, 'node_mem_total'))
    if (mem.some((p) => p.value != null)) out.push({ key: 'mem_pct', name: nameOf('monitor.metric.mem'), points: mem })
    const disk = ratioPctSeries(pointsOf(raw, 'node_disk_used'), pointsOf(raw, 'node_disk_total'))
    if (disk.some((p) => p.value != null)) out.push({ key: 'disk_pct', name: nameOf('monitor.metric.disk'), points: disk })
    return out
  }

  if (def.id === 'chunks') {
    // 分世界：同一 metricKey 下每个 world 一条线；下钻聚焦时只保留该世界。
    return raw
      .filter((x) => x.metricKey === 'world_loaded_chunks' && (x.world ?? '') !== '')
      .filter((x) => !worldFilter || x.world === worldFilter)
      .map((s) => ({ key: s.world as string, name: s.world as string, points: s.points }))
  }

  return def.sources
    .map((src) => ({ key: src.metricKey, name: nameOf(src.nameKey), points: pointsOf(raw, src.metricKey) }))
    .filter((s) => s.points.length > 0)
}

// ===== FR-221 时序剖析增强：指标目录 + 关键指标概览 + 多指标对比 =====

/** 一个可选/可对比的指标项（名称 + 单位格式）。FR-221 概览/对比据此枚举与取值。 */
export interface MetricCatalogItem {
  metricKey: string
  nameKey: string
  format: ValueFormat
}

/**
 * 各 target 的指标目录（仅列「单序列、可直接取当前值」的指标——派生占比/分世界不入目录，
 * 它们在主图网格里已有专门处理）。用于「关键指标概览」与「多指标对比」枚举可选项。
 * 严格对齐既有 FR-060 指标键（model/metric.go），不新增后端字段。
 */
export const NODE_METRIC_CATALOG: MetricCatalogItem[] = [
  { metricKey: 'node_cpu_pct', nameKey: 'monitor.metric.cpu', format: 'pct' },
  { metricKey: 'node_load', nameKey: 'monitor.metric.load1', format: 'load' },
  { metricKey: 'node_mem_used', nameKey: 'monitor.metric.memUsed', format: 'bytes' },
  { metricKey: 'node_disk_used', nameKey: 'monitor.metric.diskUsed', format: 'bytes' },
  { metricKey: 'node_net_rx_rate', nameKey: 'monitor.metric.netRx', format: 'bytesPerSec' },
  { metricKey: 'node_net_tx_rate', nameKey: 'monitor.metric.netTx', format: 'bytesPerSec' },
]

export const INSTANCE_METRIC_CATALOG: MetricCatalogItem[] = [
  { metricKey: 'inst_tps', nameKey: 'monitor.metric.tps', format: 'tps' },
  { metricKey: 'inst_mspt', nameKey: 'monitor.metric.mspt', format: 'ms' },
  { metricKey: 'inst_players_online', nameKey: 'monitor.metric.players', format: 'count' },
  { metricKey: 'inst_heap_used', nameKey: 'monitor.metric.heapUsed', format: 'bytes' },
  { metricKey: 'inst_threads', nameKey: 'monitor.metric.threads', format: 'count' },
  { metricKey: 'inst_cpu_pct', nameKey: 'monitor.metric.cpu', format: 'pct' },
]

/** 平台聚合可得的指标（/metrics/overview 仅这 4 条跨节点曲线）。 */
export const PLATFORM_METRIC_CATALOG: MetricCatalogItem[] = [
  { metricKey: 'node_cpu_pct', nameKey: 'monitor.metric.cpu', format: 'pct' },
  { metricKey: 'node_load', nameKey: 'monitor.metric.load1', format: 'load' },
  { metricKey: 'node_mem_used', nameKey: 'monitor.metric.memUsed', format: 'bytes' },
  { metricKey: 'inst_players_online', nameKey: 'monitor.metric.players', format: 'count' },
]

/** 据 target 类型取指标目录。 */
export function catalogFor(kind: 'platform' | 'node' | 'instance'): MetricCatalogItem[] {
  if (kind === 'node') return NODE_METRIC_CATALOG
  if (kind === 'instance') return INSTANCE_METRIC_CATALOG
  return PLATFORM_METRIC_CATALOG
}

/** 取一条序列在 world='' 上的最后一个非空值（关键指标「当前值」）。无则 null。 */
export function latestValue(raw: RawSeries[], metricKey: string): number | null {
  const pts = pointsOf(raw, metricKey)
  for (let i = pts.length - 1; i >= 0; i--) {
    const v = pts[i].value
    if (v != null && Number.isFinite(v)) return v
  }
  return null
}

/** 一个关键指标概览格：当前值 + 缩略趋势点。 */
export interface MetricSnapshot {
  metricKey: string
  nameKey: string
  format: ValueFormat
  current: number | null
  points: { ts: string; value: number | null }[]
}

/** 据目录与原始序列装配关键指标概览（当前值 + sparkline 点）。FR-221「关键指标概览」。 */
export function buildSnapshots(catalog: MetricCatalogItem[], raw: RawSeries[]): MetricSnapshot[] {
  return catalog.map((item) => ({
    metricKey: item.metricKey,
    nameKey: item.nameKey,
    format: item.format,
    current: latestValue(raw, item.metricKey),
    points: pointsOf(raw, item.metricKey),
  }))
}

/**
 * 装配「多指标对比」叠加曲线：按 selected 的 metricKey 顺序各取一条序列叠加到同一图。
 * 跨指标量纲不同（如 TPS vs 字节），故对比图统一不约束 Y 轴（auto），按各自原值绘制——
 * 形状/趋势对比为主，绝对值看 hover。无匹配序列的 key 跳过。
 */
export function buildCompareSeries(
  selected: string[],
  catalog: MetricCatalogItem[],
  raw: RawSeries[],
  nameOf: (key: string) => string,
): PlotSeries[] {
  const byKey = new Map(catalog.map((c) => [c.metricKey, c]))
  return selected
    .map((key) => {
      const item = byKey.get(key)
      const points = pointsOf(raw, key)
      return { key, name: item ? nameOf(item.nameKey) : key, points }
    })
    .filter((s) => s.points.length > 0)
}
