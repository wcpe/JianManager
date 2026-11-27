import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface LogRuntimeInstance {
  /**
   * 受管实例命名空间。
   *
   * 冷热双层已于 2026-10-29 废除，后端只存在 'hot'（vlsup.ValidNamespace 仅接受它）。
   * 类型保留为 string 而不是字面量 'hot'：命名空间是**后端契约**的一部分，
   * 前端写死会让后端将来新增实例类别时静默失败，而 string 会如实透传。
   */
  namespace: string
  state: string
  port: number
  listen_addr: string
  asset_tag: string
  asset_sha256: string
  last_error?: string
  health_ok: boolean
  partition_recovery_complete: boolean
  query_ready: boolean
}

export interface LogRuntimeStatus {
  supported: boolean
  instances: LogRuntimeInstance[]
  error?: { code: number; message: string }
}

export interface ApprovedLogAsset {
  tag: string
  os: string
  arch: string
  cached: boolean
  packageSha256: string
  executableSha256: string
}

/**
 * 日志运行时状态。
 *
 * 【两种生效强度】它是就绪带判定的数据源之一，但也是一次 Worker RPC + 10s 轮询：
 *  - `active=true`（正在看日志平台）：10s 轮询，状态要跟上启停；
 *  - `active=false`（就绪带常驻、看别的 sub）：**低频** 60s 轮询而不是关闭。
 *
 * 【为什么不是关掉】关掉会让就绪带的「日志平台」一条在其它 sub 下拿不到数据，
 * 只能显示「读取中…」或误判成 blocking。改造前是「永久 10s」，另一个极端；
 * 60s 足够让结论带保持最新，又把 RPC 量降到 1/6。
 */
export function useLogRuntime(nodeId: number, active: boolean) {
  return useQuery({
    queryKey: ['log-runtime', nodeId],
    queryFn: async ({ signal }) => (await api.get<LogRuntimeStatus>(`/nodes/${nodeId}/log-runtime`, { signal })).data,
    enabled: nodeId > 0,
    refetchInterval: active ? 10_000 : 60_000,
    // 就绪带要的只是「有没有在跑」，旧数据完全可以先显示再用新数据替换。
    staleTime: 30_000,
  })
}

/**
 * 审批资产清单（按 os/arch 的缓存状态）。
 *
 * 不随 sub 关闭：它是静态数据（只有上传/下发才变），却决定就绪带里「审批资产是否已下发」
 * 这一条结论。关掉它会让资产页看到一条假的「未下发」。不设轮询，靠 mutation 失效刷新。
 */
/*
  参数 `_active` 保留但不再参与 enabled：既有调用点（NodeLogRuntimeTab 等）都传它，
  改成无参会牵动一批调用方；而语义已变——审批资产是静态数据，何时都该能读到。
  下划线前缀 + 本注释说明它是有意保留的占位，不是漏用。
*/
// eslint-disable-next-line @typescript-eslint/no-unused-vars -- 保留形参以兼容既有调用签名（语义见上）
export function useApprovedLogAssets(_active: boolean) {
  return useQuery({
    queryKey: ['log-vl-assets'],
    queryFn: async ({ signal }) => (await api.get<{ assets: ApprovedLogAsset[] }>('/log-runtime/assets', { signal })).data.assets,
    enabled: true,
  })
}

export function useUploadLogAsset() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ os, arch, file }: { os: string; arch: string; file: File }) => {
      const form = new FormData()
      form.append('package', file)
      return (await api.post(`/log-runtime/assets/${os}/${arch}`, form, { timeout: 180_000 })).data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['log-vl-assets'] }),
  })
}

export function useInstallLogAsset(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => (await api.post(`/nodes/${nodeId}/log-runtime/install`, undefined, { timeout: 180_000 })).data,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['log-runtime', nodeId] })
      qc.invalidateQueries({ queryKey: ['log-vl-assets'] })
    },
  })
}

export function useControlLogRuntime(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ namespace, action }: {
      namespace: LogRuntimeInstance['namespace']
      action: 'start' | 'stop' | 'restart'
    }) => (await api.post(`/nodes/${nodeId}/log-runtime/${namespace}/${action}`)).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['log-runtime', nodeId] }),
  })
}

/**
 * 日志存储容量的原始指标键（后端 `model/metric.go` 落点）。
 *
 * 【为什么前端要自己算百分比】后端 `log_vl_disk_pct` **只在配了字节上限时**才落点：
 * 部署若用 `maxDiskUsagePercent`（百分比口径），CP 只落 `log_vl_disk_limit` 且 unit=pct，
 * pct 序列永远为空。只读 `log_vl_disk_pct` 会让「百分比部署」的节点磁盘条永久空白。
 * 所以这里结合 `unit` 区分字节口径与百分比口径，避免把 `pct` 上限误当字节。
 */
export const LOG_VL_DISK_USED = 'log_vl_disk_used'
export const LOG_VL_DISK_LIMIT = 'log_vl_disk_limit'
export const LOG_VL_DISK_PCT = 'log_vl_disk_pct'
export const LOG_VL_PARTITIONS = 'log_vl_partitions'
export const LOG_VL_ROWS_DROPPED = 'log_vl_rows_dropped'

/** 一条序列的取点数。 */
export interface LogVLMetricSeries {
  metricKey: string
  unit?: string
  points: Array<{ ts: string; avg: number | null }>
}

/** 从序列里取最后一个非空值（取数窗口内最近一次的读数）。 */
export function lastValue(series: LogVLMetricSeries[] | undefined, key: string): number | null {
  const s = series?.find((x) => x.metricKey === key)
  if (!s || s.points.length === 0) return null
  for (let i = s.points.length - 1; i >= 0; i--) {
    // noUncheckedIndexedAccess 打开：索引访问的类型含 undefined，必须先判空再取值。
    const v = s.points[i]?.avg
    if (typeof v === 'number' && Number.isFinite(v)) return v
  }
  return null
}

/**
 * 字节口径下的日志存储上限；百分比上限部署（unit=pct）返回 null，避免把「95」渲染成「95 B」。
 */
export function deriveLogDiskLimitBytes(series: LogVLMetricSeries[] | undefined): number | null {
  const limitSeries = series?.find((x) => x.metricKey === LOG_VL_DISK_LIMIT)
  const limit = lastValue(series, LOG_VL_DISK_LIMIT)
  if (limit == null || limit <= 0) return null
  const unit = limitSeries?.unit?.toLowerCase()
  if (unit === 'pct' || unit === '%') return null
  if (unit === 'bytes' || unit === 'byte' || unit === 'b') return limit
  // 缺 unit 字段的旧序列：百分比上限一定 <= 100，字节上限必然远大于 100 B。
  return limit > 100 ? limit : null
}

/**
 * 日志存储占用百分比。
 *
 * 字节口径下优先 used/limit（即使达到或超过 100% 也准确封顶为 100%）；
 * 百分比上限部署下不拿字节除以 pct，而是直接读 pct 序列。
 * 两者都拿不到时返回 null——**不返回 0**：0% 会被读成「磁盘全空」，
 * 而真实情况是「没有读数」，语义完全不同。
 */
export function deriveLogDiskPct(series: LogVLMetricSeries[] | undefined): number | null {
  const used = lastValue(series, LOG_VL_DISK_USED)
  const limitBytes = deriveLogDiskLimitBytes(series)
  if (used != null && limitBytes != null && limitBytes > 0) {
    const pct = (used / limitBytes) * 100
    return Math.max(0, Math.min(100, pct))
  }
  const direct = lastValue(series, LOG_VL_DISK_PCT)
  return direct == null ? null : Math.max(0, Math.min(100, direct))
}
