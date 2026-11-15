import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface LogRuntimeInstance {
  namespace: 'hot' | 'cold' | 'rehydrate'
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

export function useMigrateLogPartition(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    // 目标在打开确认时冻结，不能随节点面板切换而漂移；超时也不自动重放写请求。
    mutationFn: async ({ storageNamespace, utcDay, targetNodeId = nodeId }: { storageNamespace: string; utcDay: string; targetNodeId?: number }) =>
      (await api.post(`/nodes/${targetNodeId}/log-runtime/migrate`, { storageNamespace, utcDay }, { timeout: 180_000 })).data,
    retry: false,
    onSuccess: (_data, variables) => qc.invalidateQueries({ queryKey: ['log-runtime', variables.targetNodeId ?? nodeId] }),
  })
}

export function useResolveLogIngestGaps(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => (await api.post(`/nodes/${nodeId}/log-runtime/ingest/resolve-gaps`)).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['log-runtime', nodeId] }),
  })
}

/**
 * 日志存储容量的原始指标键（后端 `model/metric.go` 落点）。
 *
 * 【为什么前端要自己算百分比】后端 `log_vl_disk_pct` **只在配了字节上限时**才落点：
 * 部署若用 `maxDiskUsagePercent`（百分比口径），CP 只落 `log_vl_disk_limit` 且 unit=pct，
 * pct 序列永远为空。只读 `log_vl_disk_pct` 会让「百分比部署」的节点磁盘条永久空白。
 * 所以这里把 used 与 limit 都取回来自己算，pct 仅作兜底。
 */
export const LOG_VL_DISK_USED = 'log_vl_disk_used'
export const LOG_VL_DISK_LIMIT = 'log_vl_disk_limit'
export const LOG_VL_DISK_PCT = 'log_vl_disk_pct'
export const LOG_VL_PARTITIONS = 'log_vl_partitions'
export const LOG_VL_ROWS_DROPPED = 'log_vl_rows_dropped'

/** 一条序列的取点数。 */
export interface LogVLMetricSeries {
  metricKey: string
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
 * 日志存储占用百分比。
 *
 * 优先 used/limit（两种部署口径都成立），其次直接读 pct（旧数据/兜底）。
 * 两者都拿不到时返回 null——**不返回 0**：0% 会被读成「磁盘全空」，
 * 而真实情况是「没有读数」，语义完全不同。
 */
export function deriveLogDiskPct(series: LogVLMetricSeries[] | undefined): number | null {
  const used = lastValue(series, LOG_VL_DISK_USED)
  const limit = lastValue(series, LOG_VL_DISK_LIMIT)
  if (used != null && limit != null && limit > 0) {
    // limit 是 lastValue 的返回值（number | null），这里已收窄，故后续可直接用。
    // used 是字节、limit 是字节 → 百分比；两者都是字节单位时这才成立。
    // 百分比部署下 limit 的 unit 是 pct（值如 95），此时 used/limit 会得出荒谬的数，
    // 故用「limit 明显小于 used 一个数量级」来识别，并回落到 pct 序列。
    const pct = (used / limit) * 100
    if (pct <= 1000) return Math.max(0, Math.min(100, pct))
  }
  const direct = lastValue(series, LOG_VL_DISK_PCT)
  return direct == null ? null : Math.max(0, Math.min(100, direct))
}
