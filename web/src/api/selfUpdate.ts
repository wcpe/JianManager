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
  /** 本结果是否来自服务端缓存（FR-186）：GET /check 命中缓存为 true、空缓存为 false。 */
  cached?: boolean
  /** 「上次成功检查」时刻（FR-186，ISO 字符串）；无缓存/未检查为 undefined，前端据此展示相对时间。 */
  checkedAt?: string
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
 * 读取服务端缓存的「上次成功检查结果」（FR-186，增强 FR-081）。
 * GET /self-update/check 现返回缓存、**不触发 live 网络调用**（毫秒级），故进页即可即时回显——
 * 默认 enabled、自动拉取。live 刷新交由 {@link useRefreshSelfUpdateCheck}（进页后台静默触发 + 手动按钮）。
 * 缓存空时返回 `cached:false`，由页面据此后台触发一次刷新。
 */
export function useSelfUpdateCheck() {
  return useQuery({
    queryKey: ['self-update', 'check'],
    queryFn: async () => {
      const { data } = await api.get<CheckResult>('/self-update/check')
      return data
    },
    retry: false,
    // 缓存读取无副作用，但仍不长驻轮询：进页拉一次缓存即可，刷新由 refresh mutation 驱动。
    refetchOnWindowFocus: false,
  })
}

/**
 * 显式触发一次 live 检查更新（POST /self-update/check/refresh，FR-186）。
 * 经更新源在线拉取最新版本 + 逐节点 RPC 取版本，成功后服务端覆盖缓存；本端把返回写回 check 查询缓存，
 * 页面随即更新。失败时服务端**不清缓存**、本端亦保留旧 check 数据（调用方 toast 提示但不清屏）。
 * 「检查更新」按钮与进页后台静默刷新均调此。
 */
export function useRefreshSelfUpdateCheck() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const { data } = await api.post<CheckResult>('/self-update/check/refresh')
      return data
    },
    onSuccess: (data) => {
      // 刷新成功：把最新结果写回 check 查询缓存，驱动页面即时更新（含 checkedAt）。
      qc.setQueryData(['self-update', 'check'], data)
    },
    // 刷新失败不触碰 check 缓存：保留进页时读到的旧数据（断网/限流仍可见上次结果 + 上次检查时间）。
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
