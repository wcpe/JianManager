import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 面板自更新 API（FR-081，见 ADR-020）。
 * 仅平台管理员可用（后端挂在 admin 组、RBAC 强制）。检查更新对比 CP 自身 + 各节点版本；
 * 升级为 CP 自更新 / 单节点 / 全网逐节点编排（rollout）。
 */

/** 单个组件（CP 或某节点）的版本对比结果。 */
export interface ComponentStatus {
  /** 节点 ID；CP 自身为 0（字段省略）。 */
  nodeId?: number
  nodeUuid?: string
  name?: string
  online: boolean
  currentVersion: string
  os: string
  arch: string
  /** feed 最新版与当前版不同且有匹配制品时为 true。 */
  updateAvailable: boolean
  /** feed 中存在匹配该组件平台（component+os+arch）的制品。 */
  artifactAvailable: boolean
  /** 升级前备份的版本（FR-182）：非空时可一键「回滚 v{backupVersion}」，空表示无备份。 */
  backupVersion?: string
}

/** GET /self-update/check 的返回。 */
export interface CheckResult {
  /** 是否已配置更新源（github_repo 或 feed_url 非空）。 */
  configured: boolean
  latestVersion: string
  notes: string
  /** 更新源标识（FR-175）：github:owner/repo@channel | feed | 空（未配置）。 */
  source?: string
  controlPlane: ComponentStatus
  nodes: ComponentStatus[]
}

/** rollout 中单个节点的升级状态。 */
export interface RolloutNodeState {
  nodeId: number
  name: string
  /** pending | upgrading | succeeded | failed */
  state: string
  fromVersion: string
  toVersion: string
  error: string
  attempts: number
}

/** 一次全网逐节点升级编排的进度快照。 */
export interface Rollout {
  rolloutId: string
  targetVersion: string
  /** idle | running | completed */
  state: string
  startedAt: string
  finishedAt: string | null
  total: number
  succeeded: number
  failed: number
  pending: number
  nodes: RolloutNodeState[]
}

/** CP 自更新接受响应（202）。 */
export interface ControlPlaneUpgradeAck {
  status: string
  fromVersion: string
  toVersion: string
}

/** 单节点升级接受响应（202）。 */
export interface NodeUpgradeAck {
  status: string
  nodeId: number
  fromVersion: string
  toVersion: string
}

/** CP 回滚接受响应（202，FR-182）。 */
export interface ControlPlaneRollbackAck {
  status: string
  fromVersion: string
  toVersion: string
}

/** 单节点回滚接受响应（202，FR-182）。 */
export interface NodeRollbackAck {
  status: string
  nodeId: number
  fromVersion: string
  toVersion: string
}

/**
 * 检查更新（CP + 各节点版本对比）。
 * 不自动轮询：检查会实时拉取 feed 并逐节点 RPC 取版本，开销较大，由用户手动触发刷新。
 */
export function useSelfUpdateCheck() {
  return useQuery({
    queryKey: ['self-update', 'check'],
    queryFn: async () => {
      const { data } = await api.get<CheckResult>('/self-update/check')
      return data
    },
    enabled: false,
    retry: false,
  })
}

/** 当前/最近一次全网升级进度；rollout 运行中自动短轮询（2s），空闲/完成后停止。 */
export function useRollout() {
  return useQuery({
    queryKey: ['self-update', 'rollout'],
    queryFn: async () => {
      const { data } = await api.get<Rollout>('/self-update/rollout')
      return data
    },
    refetchInterval: (query) => (query.state.data?.state === 'running' ? 2000 : false),
  })
}

/** 升级 CP 自身（下载→校验→替换→平滑重启）；留空 version 取 feed 最新。 */
export function useUpgradeControlPlane() {
  return useMutation({
    mutationFn: (version?: string) =>
      api.post<ControlPlaneUpgradeAck>('/self-update/control-plane/upgrade', { version }).then((r) => r.data),
  })
}

/** 升级单个 Worker 节点（经 CP gRPC 编排）。 */
export function useUpgradeNode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ nodeId, version }: { nodeId: number; version?: string }) =>
      api.post<NodeUpgradeAck>(`/self-update/nodes/${nodeId}/upgrade`, { version }).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['self-update', 'check'] }),
  })
}

/** 发起全网逐节点升级编排（nodeIds 省略=全部在线节点）。 */
export function useUpgradeAll() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ nodeIds, version }: { nodeIds?: number[]; version?: string }) =>
      api.post<Rollout>('/self-update/nodes/upgrade-all', { nodeIds, version }).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['self-update', 'rollout'] }),
  })
}

/** 回滚 CP 自身到升级前备份（校验备份→换回→平滑重启，FR-182）。 */
export function useRollbackControlPlane() {
  return useMutation({
    mutationFn: () =>
      api.post<ControlPlaneRollbackAck>('/self-update/control-plane/rollback').then((r) => r.data),
  })
}

/** 回滚单个 Worker 节点到其升级前备份（经 CP gRPC，Worker 走本地备份，FR-182）。 */
export function useRollbackNode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ nodeId }: { nodeId: number }) =>
      api.post<NodeRollbackAck>(`/self-update/nodes/${nodeId}/rollback`).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['self-update', 'check'] }),
  })
}
