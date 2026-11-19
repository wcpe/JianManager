import { keepPreviousData, useQuery } from '@tanstack/react-query'
import api from '@/api/client'
import type { ManagedConsistency, ManagedConnection, ManagedConfiguration, ManagedTracedKey } from '@/api/managedConfig'

/**
 * 探针托管配置的对账矩阵（期望 vs 观测）。
 *
 * 契约真源是后端 `internal/controlplane/service/managed_state.go` 与
 * `internal/controlplane/router/managed_config.go`。
 *
 * 【三条由后端定义、前端不得自行重算的口径】
 * 1. `desiredVersion` / `cacheAgeSeconds` 是**可空**的：null 与 0 语义相反
 *    （null = 从未下发 / 从未生效，0 = 刚下发了初始版本 / 刚刚生效）。
 *    前端把 null 渲染成 0 就是把「没托管过」说成「刚同步完成」。
 * 2. 页头汇总接口**不受 consistency 筛选影响**（后端已显式清空该维），
 *    所以点进「漂移」后其它档位不会归零。前端不得自己按当前列表统计——那样必然归零。
 * 3. 连接状态是**合成值**（桥连接事件优先），与探针自报的 `reportedConnection` 不是一回事；
 *    筛选作用在合成值上，展示也必须用合成值，否则「筛出来的」和「看到的」会不一致。
 */

// ---------------------------------------------------------------------------
// 实体（与 service.ManagedMatrixRow / Summary / Detail 逐字段对应）
// ---------------------------------------------------------------------------

/** 矩阵的一行（每实例一行）。 */
export interface ManagedMatrixRow {
  instanceId: number
  instanceUuid: string
  instanceName: string
  nodeId: number
  nodeName?: string
  /** CP 侧最近一次下发的目标版本；**从未下发为 null，不是 0**。 */
  desiredVersion: number | null
  /** 探针上报的版本；从未上报为 null。 */
  observedVersion: number | null
  mode?: string
  /** 合成后的连接状态。 */
  connection: ManagedConnection | string
  /** 探针自报的连接态（**可能因断连而过期**，仅供排障）。 */
  reportedConnection?: string
  /** 距上次成功应用的秒数；**null = 从未生效过**，0 = 刚刚生效。 */
  cacheAgeSeconds: number | null
  lastError?: string
  reportedAt?: string | null
  consistency: ManagedConsistency | string
  lastTargetState?: string
  lastPushError?: string
  /** 配置来源：file / cp。 */
  configuration: ManagedConfiguration | string
}

/** 交叉矩阵的一格（配置来源 × 连接状态）。 */
export interface ManagedMatrixCell {
  configuration: string
  connection: string
  count: number
}

/** 汇总（页头与两维展示的数据源）。 */
export interface ManagedMatrixSummary {
  total: number
  byConsistency: Record<string, number>
  byConnection: Record<string, number>
  byConfiguration: Record<string, number>
  crossMatrix: ManagedMatrixCell[]
}

/** 单实例下钻明细。 */
export interface ManagedMatrixDetail {
  row: ManagedMatrixRow
  /** 最近一次下发冻结的 values（CP 期望的配置）。 */
  desiredValues: Record<string, unknown>
  /** 探针回报的生效值。 */
  observedEffective: Record<string, unknown>
  /** 逐键来源（哪个 scope、哪一版）。 */
  traced: ManagedTracedKey[]
  /** 最近若干次下发经历。 */
  recentPushes: Array<{
    id: number
    sessionId: number
    instanceId: number
    batchNo: number
    state: string
    desiredVersion: number
    baseVersion: number
    ackVersion?: number | null
    errorKind?: string
    error?: string
    attempts: number
    createdAt: string
    updatedAt: string
  }>
}

/** 矩阵查询参数（全部下推 SQL，服务端分页）。 */
export interface ManagedMatrixParams {
  /** 实例名/UUID 模糊搜索。后端读的是 `keyword`（不是 `search`）。 */
  keyword?: string
  consistency?: string
  connection?: string
  configuration?: string
  nodeId?: number
  page?: number
  pageSize?: number
}

// ---------------------------------------------------------------------------
// 纯函数
// ---------------------------------------------------------------------------

/** 一致性档位顺序（页头 chip 顺序，按「越需要处理越靠前」）。 */
export const CONSISTENCY_ORDER: ManagedConsistency[] = ['drift', 'failed', 'neverReported', 'inSync']

/** 连接状态顺序（四态）。 */
export const CONNECTION_ORDER: ManagedConnection[] = ['connected', 'degraded', 'unauthorized', 'disconnected']

/** 配置来源顺序（cp 优先：已托管的先看）。 */
export const CONFIGURATION_ORDER: ManagedConfiguration[] = ['cp', 'file']

/** 一致性 → 状态色等级。 */
export function consistencyLevel(value: string): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  switch (value) {
    case 'inSync':
      return 'success'
    case 'drift':
      return 'warning'
    case 'failed':
      return 'danger'
    case 'neverReported':
      return 'neutral'
    default:
      return 'neutral'
  }
}

/** 连接状态 → 状态色等级。`unauthorized` 是需要告警的异常（控制面拒了凭据），按 danger。 */
export function connectionLevel(value: string): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  switch (value) {
    case 'connected':
      return 'success'
    case 'degraded':
      return 'warning'
    case 'unauthorized':
      return 'danger'
    case 'disconnected':
      return 'neutral'
    default:
      return 'neutral'
  }
}

/**
 * 期望版本的展示文案。
 *
 * **null 必须与 0 区分**：0 是探针首次托管前的合法版本号，
 * 把它和「从未下发」混在一列会让「没托管过」看起来像「下发了初始版本」。
 */
export function formatDesiredVersion(value: number | null | undefined, notPushedLabel: string): string {
  if (value === null || value === undefined) return notPushedLabel
  return String(value)
}

/** 观测版本的展示文案（null = 从未上报）。 */
export function formatObservedVersion(value: number | null | undefined, neverReportedLabel: string): string {
  if (value === null || value === undefined) return neverReportedLabel
  return String(value)
}

/** 缓存年龄的展示文案：null → 占位符（**不能显示 0**，0 表示刚刚生效）。 */
export function formatCacheAge(value: number | null | undefined, emptyLabel: string): string {
  if (value === null || value === undefined) return emptyLabel
  return String(value)
}

/**
 * 断连但自报陈旧的提示：桥没连上时，探针自报值必然是断连前的旧值。
 *
 * 返回 null 表示不需要提示（连接正常，或压根没有上报时刻可引用）。
 */
export function staleBridgeNote(
  row: Pick<ManagedMatrixRow, 'connection' | 'reportedAt'>,
  relative: string,
): string | null {
  if (row.connection !== 'disconnected') return null
  if (!row.reportedAt) return null
  if (!relative) return null
  return relative
}

/** 一致性筛选的「一键筛漂移」判定：当前是否已在看漂移。 */
export function isDriftFilter(consistency: string | undefined): boolean {
  return consistency === 'drift'
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

/** 矩阵分页列表（GET /managed-matrix）。 */
export function useManagedMatrix(params: ManagedMatrixParams) {
  return useQuery({
    queryKey: ['managed-matrix', 'rows', params],
    queryFn: async () => {
      const { data } = await api.get<{
        items: ManagedMatrixRow[]
        total: number
        page: number
        pageSize: number
      }>('/managed-matrix', { params })
      return {
        items: data.items ?? [],
        total: data.total ?? 0,
        page: data.page ?? 1,
        pageSize: data.pageSize ?? 20,
      }
    },
    placeholderData: keepPreviousData,
  })
}

/**
 * 汇总（GET /managed-matrix/summary）。
 *
 * 刻意**只带** nodeId / keyword 之外的展示无关参数：后端已把 consistency 清空，
 * 传了也会被忽略；这里只把节点与关键字下推，保证「盯着某个节点时汇总也是该节点的」。
 */
export function useManagedMatrixSummary(params: Pick<ManagedMatrixParams, 'keyword' | 'nodeId'>) {
  return useQuery({
    queryKey: ['managed-matrix', 'summary', params],
    queryFn: async () => {
      const { data } = await api.get<ManagedMatrixSummary>('/managed-matrix/summary', { params })
      return {
        total: data.total ?? 0,
        byConsistency: data.byConsistency ?? {},
        byConnection: data.byConnection ?? {},
        byConfiguration: data.byConfiguration ?? {},
        crossMatrix: data.crossMatrix ?? [],
      }
    },
  })
}

/** 单实例下钻明细（GET /managed-matrix/instances/:id）。 */
export function useManagedMatrixDetail(instanceId: number | null) {
  return useQuery({
    queryKey: ['managed-matrix', 'detail', instanceId],
    queryFn: async () => {
      const { data } = await api.get<ManagedMatrixDetail>(`/managed-matrix/instances/${instanceId}`)
      return data
    },
    enabled: !!instanceId,
  })
}
