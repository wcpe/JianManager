import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { apiErrorMessage } from '@/lib/shared/api-error'

/**
 * 配置分批灰度与交付编排（FR-531）与生效编排（FR-532）。
 *
 * 契约见 `docs/API.md`、`docs/specs/config-write-grayscale-activation/spec.md`
 * 与后端 `internal/controlplane/service/config_delivery.go`。
 *
 * 【三条前端必须诚实体现在界面上的语义】
 * 1. `deferred` **不是** `failed`：离线目标不计入失败率（分母排除 deferred），上线后自动补推。
 *    两者合起来显示会让运维误判「配置坏了」，实际只是机器不在。
 * 2. 熔断后单状态是 `paused` 且 `pauseKind=circuit_break`，不是 `failed`——
 *    「被系统停下等人决策」与「变更失败」是两件事。
 * 3. `bestEffort` 目标即使 `status=activated` 也**不能**显示成「已生效」：
 *    `activateConfirmed=false` + `bestEffort=true` 意味着插件没给确切回执
 *    （Bukkit 的 `reloadConfig()` 返回 void），只能写「已请求重载，插件未提供确认」。
 */

// ---------------------------------------------------------------------------
// 枚举（与 internal/controlplane/model/config_delivery.go 逐值对应）
// ---------------------------------------------------------------------------

/** 交付单级状态。 */
export type DeliveryStatus =
  | 'draft'
  | 'pending_approval'
  | 'approved'
  | 'rolling'
  | 'observing'
  | 'paused'
  | 'failed'
  | 'completed'
  | 'rolling_back'
  | 'rolled_back'
  | 'cancelled'

/** 目标级状态。 */
export type DeliveryTargetStatus =
  | 'pending'
  | 'pushing'
  | 'pushed'
  | 'activating'
  | 'activated'
  | 'failed'
  | 'deferred'
  | 'skipped'

/** 分批模式：percent（batchSizes 形如 [10,30,60] 总和 ≤100）/ count（形如 [1,5,20]）。 */
export type DeliveryBatchMode = 'percent' | 'count'

/** 生效方式。 */
export type ActivationMethod = 'hot_reload' | 'restart' | 'push_only'

/** 暂停原因分类：人工 / 熔断。 */
export type DeliveryPauseKind = 'manual' | 'circuit_break'

/** 继续模式：重试失败目标 / 跳过失败目标。 */
export type DeliveryResumeMode = 'retry_failed' | 'skip_failed'

/** 层作用域级别（与 FR-529 的 ConfigScopeLevel 对应）。 */
export type ConfigScopeLevel = 'group' | 'instance'

// ---------------------------------------------------------------------------
// 实体
// ---------------------------------------------------------------------------

/** 交付单（后端 `model.ConfigDeliveryOrder`）。 */
export interface ConfigDeliveryOrder {
  id: number
  configFileId: number
  /** 落盘目标路径（相对实例工作目录）。 */
  filePath: string
  targetLayerLevel: ConfigScopeLevel
  targetLayerScopeId: number
  /** 提交时刻该层的 head 版本（审计用：到底应用了哪一版）。 */
  targetLayerVersionId: number
  /** 变更前的层 head（整批回滚的版本回退目标）。 */
  fromLayerVersionId?: number | null
  batchMode: DeliveryBatchMode
  /** 批间观察窗（秒）。 */
  observeWindowSec: number
  /** 失败率阈值 %，0=关闭。 */
  failureRateThreshold: number
  /** 不健康率阈值 %，0=关闭。 */
  unhealthyRateThreshold: number
  activationMethod: ActivationMethod
  activateTimeoutSec: number
  reloadTimeoutSec: number
  /** 离线目标上线后是否自动补推。 */
  autoRetryDeferred: boolean
  deferredRetryLimit: number

  status: DeliveryStatus
  /** 仅 `status=paused` 时有意义：manual / circuit_break。 */
  pauseKind?: string
  pauseReason?: string
  /** 熔断现场：哪条阈值、阈值多少、实测多少。 */
  breakReason?: string
  breakThreshold?: number
  breakObserved?: number

  plannedCount: number
  pushedCount: number
  activatedCount: number
  failedCount: number
  /** 离线/暂不可达，**不计入失败率**。 */
  deferredCount: number
  skippedCount: number
  currentBatch: number
  totalBatches: number
  failedBatchNo?: number

  createdBy: number
  createdAt: string
  updatedAt: string
  startedAt?: string | null
  finishedAt?: string | null
}

/** 单个交付目标的对外视图。 */
export interface DeliveryTargetView {
  id: number
  orderId: number
  instanceId: number
  instanceName: string
  batchNo: number
  status: DeliveryTargetStatus
  /** 人读原因。 */
  reason?: string
  /** 机器可读原因码（PATH_ESCAPE / WRITE_VERIFY_FAILED / TIMEOUT / BEST_EFFORT / CIRCUIT_BREAK…）。 */
  reasonCode?: string
  changedFileCount: number
  backupPresent: boolean
  backupPath?: string
  previousVersionId?: number | null
  newVersionId?: number | null
  /** 落盘是否成功。**落盘成功 ≠ 生效成功**（FR-532 §16）。 */
  diskWritten: boolean
  /** 生效是否得到确切确认。`bestEffort=true` 时恒为 false。 */
  activateConfirmed: boolean
  /** 插件未提供确切回执（Bukkit reloadConfig() 是 void）。 */
  bestEffort: boolean
  /** 后端给出的诚实文案（已请求重载，插件未提供确认 / 重载超时…）。 */
  activationMessage?: string
  /** deferred 补推次数。 */
  retryCount: number
  rolledBack: boolean
  rollbackOfRollback: boolean
  startedAt?: string | null
  finishedAt?: string | null
  /** 实例当前状态（实时读库，便于判断「离线」是不是临时状态）。 */
  instanceStatus?: string
}

/** 影响预览里的单台明细。 */
export interface DeliveryImpactTarget {
  instanceId: number
  instanceName: string
  online: boolean
  changed: boolean
  currentHash?: string
  targetHash?: string
  error?: string
}

/** 影响预览（GET /config-deliveries/:id/impact）。 */
export interface DeliveryImpact {
  orderId: number
  planned: number
  online: number
  offline: number
  changed: number
  unchanged: number
  /** 合并有效配置失败、无法判定差异的台数。 */
  undetermined: number
  /** 离线率 %（批开始时的熔断判定依据）。 */
  offlineRate: number
  /** 按当前离线率是否会直接熔断。 */
  wouldCircuitBreak: boolean
  /** 逐台明细（后端上限 500）。 */
  targets: DeliveryImpactTarget[]
  truncated: boolean
}

/** 观察窗实测口径。 */
export interface DeliveryObserveMeasured {
  planned: number
  failed: number
  /** 离线挂起台数；**失败率分母排除它们**。 */
  deferred: number
  /** 失败率 %。 */
  failureRate: number
  /** 已生效目标中实例当前非 RUNNING 的数量。 */
  unhealthy: number
  unhealthyRate: number
}

/** 逐批状态（批是记账单位，不是实体）。 */
export interface DeliveryObserveBatch {
  batchNo: number
  state: string
  total: number
  activated: number
  failed: number
  deferred: number
  skipped: number
}

/** 观察窗数据（GET /config-deliveries/:id/observe）。 */
export interface DeliveryObserve {
  orderId: number
  status: DeliveryStatus
  currentBatch: number
  totalBatches: number
  thresholds: {
    failureRateThreshold: number
    unhealthyRateThreshold: number
    /** 离线率熔断线（后端固定 10%）。 */
    offlineBreakPercent: number
    observeWindowSec: number
  }
  measured: DeliveryObserveMeasured
  batches: DeliveryObserveBatch[]
}

/** 回滚的单台结果。 */
export interface DeliveryRollbackTargetResult {
  instanceId: number
  success: boolean
  error?: string
}

/** 回滚结果（POST /config-deliveries/:id/rollback）。 */
export interface DeliveryRollbackResult {
  orderId: number
  rollbackId: number
  /** `full`（整批，含层版本回退）/ `subset`（目标级子集，版本不回退）。 */
  scope: 'full' | 'subset'
  succeeded: number
  failed: number
  /** 是否执行了配置层版本回退。 */
  layerRolledBack: boolean
  /** 本次是否为「回滚的回滚」。 */
  rollbackOfRollback: boolean
  targets: DeliveryRollbackTargetResult[]
}

/** 创建/编辑交付单的入参。 */
export interface ConfigDeliveryInput {
  configFileId: number
  /** 落盘路径；留空时后端取 ConfigFile.name。 */
  filePath?: string
  targetLayerLevel: ConfigScopeLevel
  targetLayerScopeId: number
  selector: {
    groupIds?: number[]
    includeDescendants?: boolean
    instanceIds?: number[]
    tags?: string[]
    nodeIds?: number[]
  }
  batchMode: DeliveryBatchMode
  batchSizes: number[]
  observeWindowSec?: number
  failureRateThreshold?: number
  unhealthyRateThreshold?: number
  activationMethod: ActivationMethod
  activateTimeoutSec?: number
  reloadTimeoutSec?: number
  /** 省略 = 后端默认 true；显式 false 才是「关闭补推」。 */
  autoRetryDeferred?: boolean
  deferredRetryLimit?: number
}

/** 交付单分页响应（后端 `gin.H{"orders", "total", "page", "pageSize"}`）。 */
interface DeliveryListResponse {
  orders: ConfigDeliveryOrder[]
  total: number
  page: number
  pageSize: number
}

/** 目标分页响应（后端 `gin.H{"targets", "total", "page", "pageSize"}`）。 */
interface DeliveryTargetsResponse {
  targets: DeliveryTargetView[]
  total: number
  page: number
  pageSize: number
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

/** 分页列出交付单（GET /config-deliveries）。 */
export function useConfigDeliveries(params: { status?: string; q?: string; page?: number; pageSize?: number } = {}) {
  return useQuery({
    queryKey: ['config-deliveries', params],
    queryFn: async () => {
      const { data } = await api.get<DeliveryListResponse>('/config-deliveries', { params })
      return {
        orders: data.orders ?? [],
        total: data.total ?? 0,
        page: data.page ?? 1,
        pageSize: data.pageSize ?? 20,
      }
    },
    placeholderData: keepPreviousData,
  })
}

/** 交付单详情（GET /config-deliveries/:id）。 */
export function useConfigDelivery(id: number | null) {
  return useQuery({
    queryKey: ['config-deliveries', id],
    queryFn: async () => {
      const { data } = await api.get<ConfigDeliveryOrder>(`/config-deliveries/${id}`)
      return data
    },
    enabled: !!id,
  })
}

/** 目标分页（GET /config-deliveries/:id/targets）。 */
export function useConfigDeliveryTargets(
  id: number | null,
  params: { status?: string; batch?: number; page?: number; pageSize?: number } = {},
) {
  return useQuery({
    queryKey: ['config-deliveries', id, 'targets', params],
    queryFn: async () => {
      const { data } = await api.get<DeliveryTargetsResponse>(`/config-deliveries/${id}/targets`, { params })
      return {
        targets: data.targets ?? [],
        total: data.total ?? 0,
        page: data.page ?? 1,
        pageSize: data.pageSize ?? 50,
      }
    },
    enabled: !!id,
    placeholderData: keepPreviousData,
  })
}

/** 影响预览（GET /config-deliveries/:id/impact）。 */
export function useConfigDeliveryImpact(id: number | null, enabled = true) {
  return useQuery({
    queryKey: ['config-deliveries', id, 'impact'],
    queryFn: async () => {
      const { data } = await api.get<DeliveryImpact>(`/config-deliveries/${id}/impact`)
      return data
    },
    enabled: !!id && enabled,
  })
}

/** 观察窗数据（GET /config-deliveries/:id/observe）。 */
export function useConfigDeliveryObserve(id: number | null, enabled = true) {
  return useQuery({
    queryKey: ['config-deliveries', id, 'observe'],
    queryFn: async () => {
      const { data } = await api.get<DeliveryObserve>(`/config-deliveries/${id}/observe`)
      return data
    },
    enabled: !!id && enabled,
  })
}

// ---------------------------------------------------------------------------
// 写动作
// ---------------------------------------------------------------------------

/**
 * 状态推进类动作的公共失效范围。
 *
 * 状态机一条动作会同时改写单、目标与计数，三者都由同一 queryKey 前缀派生；
 * 只失效单本身会让目标列表与观察窗继续显示推进前的状态。
 */
function invalidateOrder(qc: ReturnType<typeof useQueryClient>, id: number) {
  void qc.invalidateQueries({ queryKey: ['config-deliveries'] })
  void qc.invalidateQueries({ queryKey: ['config-deliveries', id] })
}

/** 可推进的动作名（状态推进类）。 */
type DeliveryActionName = 'submit' | 'approve' | 'start' | 'pause' | 'resume' | 'cancel' | 'rollback'

/** 逐个动作的 toast 文案（成功）。 */
const ACTION_SUCCESS: Record<DeliveryActionName, string> = {
  submit: '已提交审批',
  approve: '已批准',
  start: '已开始放量',
  pause: '已暂停',
  resume: '已继续',
  cancel: '已终止',
  rollback: '已执行回滚',
}

/** 逐个动作的兜底失败文案。 */
const ACTION_FALLBACK: Record<DeliveryActionName, string> = {
  submit: '提交审批失败',
  approve: '批准失败',
  start: '开始放量失败',
  pause: '暂停失败',
  resume: '继续失败',
  cancel: '终止失败',
  rollback: '回滚失败',
}

/**
 * 动作 mutation 的公共工厂。
 *
 * 刻意**不做乐观更新**：这些动作的真实效果由后端异步推进（`start` 返回 202 后
 * 后台才逐批下发），先本地改状态再回退会让人误以为变更已经落地。
 */
function useDeliveryAction<TVars>(action: DeliveryActionName, run: (vars: TVars) => Promise<unknown>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: run,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['config-deliveries'] })
      toast.success(ACTION_SUCCESS[action])
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, ACTION_FALLBACK[action]))
    },
  })
}

/** 创建交付单（POST /config-deliveries）。 */
export function useCreateConfigDelivery() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: ConfigDeliveryInput) => {
      const { data } = await api.post<ConfigDeliveryOrder>('/config-deliveries', body)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['config-deliveries'] })
      toast.success('交付单已创建（草稿）')
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, '创建交付单失败'))
    },
  })
}

/** 编辑交付单（PUT /config-deliveries/:id，仅 draft）。 */
export function useUpdateConfigDelivery() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, body }: { id: number; body: ConfigDeliveryInput }) => {
      const { data } = await api.put<ConfigDeliveryOrder>(`/config-deliveries/${id}`, body)
      return data
    },
    onSuccess: (_data, vars) => {
      void qc.invalidateQueries({ queryKey: ['config-deliveries'] })
      void qc.invalidateQueries({ queryKey: ['config-deliveries', vars.id] })
      toast.success('交付单已保存')
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, '保存交付单失败'))
    },
  })
}

/** 删除交付单（DELETE /config-deliveries/:id，仅 draft）。 */
export function useDeleteConfigDelivery() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await api.delete(`/config-deliveries/${id}`)
      return id
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['config-deliveries'] })
      toast.success('交付单已删除')
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, '删除交付单失败'))
    },
  })
}

/** 提交审批（draft → pending_approval）。 */
export function useSubmitConfigDelivery() {
  return useDeliveryAction<number>('submit', async (id) => {
    const { data } = await api.post<ConfigDeliveryOrder>(`/config-deliveries/${id}/submit`)
    return data
  })
}

/** 批准（pending_approval → approved）。 */
export function useApproveConfigDelivery() {
  return useDeliveryAction<number>('approve', async (id) => {
    const { data } = await api.post<ConfigDeliveryOrder>(`/config-deliveries/${id}/approve`)
    return data
  })
}

/** 开始放量（approved → rolling，异步执行）。 */
export function useStartConfigDelivery() {
  return useDeliveryAction<number>('start', async (id) => {
    // 后端返回 202 + 当前单；忽略响应体，状态由随后的失效重取呈现。
    await api.post(`/config-deliveries/${id}/start`)
  })
}

/** 暂停（rolling/observing → paused）。`reason` 走 query 参数（后端读 `?reason=`）。 */
export function usePauseConfigDelivery() {
  return useDeliveryAction<{ id: number; reason?: string }>('pause', async ({ id, reason }) => {
    const { data } = await api.post<ConfigDeliveryOrder>(`/config-deliveries/${id}/pause`, null, {
      params: { reason },
    })
    return data
  })
}

/** 继续（paused → rolling）。`mode` 走请求体（后端优先读 body.mode）。 */
export function useResumeConfigDelivery() {
  return useDeliveryAction<{ id: number; mode: DeliveryResumeMode }>('resume', async ({ id, mode }) => {
    const { data } = await api.post<ConfigDeliveryOrder>(`/config-deliveries/${id}/resume`, { mode })
    return data
  })
}

/** 终止（→ cancelled；未下发目标置 skipped）。 */
export function useCancelConfigDelivery() {
  return useDeliveryAction<{ id: number; reason?: string }>('cancel', async ({ id, reason }) => {
    const { data } = await api.post<ConfigDeliveryOrder>(`/config-deliveries/${id}/cancel`, { reason })
    return data
  })
}

/**
 * 回滚（POST /config-deliveries/:id/rollback）。
 *
 * `instanceIds` 为空 = **整批回滚**（逐台还原快照 + 配置层版本回退 + 重新生效）；
 * 非空 = **目标级子集回滚**（只还原这几台，**层版本不回退**，§10.2）。
 * 两者语义不同，界面必须让用户明确选中范围后再提交。
 */
export function useRollbackConfigDelivery() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({
      id,
      instanceIds,
      reason,
    }: {
      id: number
      instanceIds?: number[]
      reason?: string
    }) => {
      const { data } = await api.post<DeliveryRollbackResult>(`/config-deliveries/${id}/rollback`, {
        instanceIds,
        reason,
      })
      return data
    },
    onSuccess: (res, vars) => {
      invalidateOrder(qc, vars.id)
      toast.success(
        res.scope === 'full'
          ? `整批回滚完成：成功 ${res.succeeded}，失败 ${res.failed}`
          : `子集回滚完成：成功 ${res.succeeded}，失败 ${res.failed}`,
      )
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, '回滚失败'))
    },
  })
}

// ---------------------------------------------------------------------------
// 纯函数（界面与测试共用；不依赖 React）
// ---------------------------------------------------------------------------

/** 交付单状态的 i18n 键后缀（`configDeliveries.status.<key>`）。 */
export const DELIVERY_STATUS_KEYS: Record<DeliveryStatus, string> = {
  draft: 'draft',
  pending_approval: 'pendingApproval',
  approved: 'approved',
  rolling: 'rolling',
  observing: 'observing',
  paused: 'paused',
  failed: 'failed',
  completed: 'completed',
  rolling_back: 'rollingBack',
  rolled_back: 'rolledBack',
  cancelled: 'cancelled',
}

/** 目标状态的 i18n 键后缀。 */
export const DELIVERY_TARGET_STATUS_KEYS: Record<DeliveryTargetStatus, string> = {
  pending: 'pending',
  pushing: 'pushing',
  pushed: 'pushed',
  activating: 'activating',
  activated: 'activated',
  failed: 'failed',
  deferred: 'deferred',
  skipped: 'skipped',
}

/** 状态 → StatusLevel（配色等级，与组件库 StatusBadge 共用一套语义色）。 */
export function deliveryStatusLevel(status: DeliveryStatus): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  switch (status) {
    case 'completed':
      return 'success'
    case 'rolling':
    case 'observing':
    case 'rolling_back':
      return 'info'
    case 'paused':
    case 'pending_approval':
      return 'warning'
    case 'failed':
      return 'danger'
    case 'rolled_back':
    case 'cancelled':
      return 'neutral'
    default:
      return 'neutral'
  }
}

/** 目标状态 → StatusLevel。`deferred` 是 warning 而非 danger：目标只是不在线。 */
export function deliveryTargetStatusLevel(
  status: DeliveryTargetStatus,
): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  switch (status) {
    case 'activated':
      return 'success'
    case 'deferred':
      return 'warning'
    case 'failed':
      return 'danger'
    case 'pushing':
    case 'activating':
    case 'pushed':
      return 'info'
    default:
      return 'neutral'
  }
}

/** 交付单当前是否被熔断停下（`paused` + `circuit_break`）。 */
export function isCircuitBreakPaused(order: Pick<ConfigDeliveryOrder, 'status' | 'pauseKind'>): boolean {
  return order.status === 'paused' && order.pauseKind === 'circuit_break'
}

/**
 * 目标是否「已生效且得到确认」。
 *
 * 只在 `activated` **且** `activateConfirmed` 时成立。`bestEffort` 目标虽然也记
 * `activated`（§14.1 明确如此），但那只是「请求已下发」，插件没给回执——
 * 任何把它渲染成「已生效」的文案都是夸大。
 */
export function isActivationConfirmed(
  target: Pick<DeliveryTargetView, 'status' | 'activateConfirmed' | 'bestEffort'>,
): boolean {
  return target.status === 'activated' && target.activateConfirmed && !target.bestEffort
}

/** 状态机可用动作（纯函数，便于逐条单测）。 */
export interface DeliveryActions {
  submit: boolean
  approve: boolean
  start: boolean
  pause: boolean
  resume: boolean
  cancel: boolean
  rollback: boolean
  /** 仅草稿可编辑。 */
  edit: boolean
  /** 仅草稿可删除。 */
  remove: boolean
}

/**
 * 由当前状态推导可用动作。
 *
 * 判据全部取自后端状态机表（`model.deliveryOrderTransitions`）与各方法的前置校验，
 * **不在前端自创规则**：按钮可点性必须与后端「会不会拒绝」一致，否则用户点下去只会拿到
 * 409 STATE_CONFLICT。逐个对应：
 * - `submit`：draft → pending_approval
 * - `approve`：pending_approval → approved
 * - `start`：只有 approved 能开跑（`Start` 里显式校验）
 * - `pause`：paused 是 rolling/observing 的合法去向
 * - `resume`：只有 paused 能继续（`Resume` 里显式校验）
 * - `cancel`：paused 可终止；rolling/observing/approved/pending_approval 亦允许（后端 `Cancel`
 *   只拒绝已回滚、已完成、已失败三个终态）
 * - `rollback`：failed / completed / paused / rolling / observing 可回滚；
 *   已回滚状态可再次回滚（§10.3「回滚本身也可回滚」）
 */
export function deliveryActionsOf(status: DeliveryStatus): DeliveryActions {
  return {
    submit: status === 'draft',
    approve: status === 'pending_approval',
    start: status === 'approved',
    pause: status === 'rolling' || status === 'observing',
    resume: status === 'paused',
    cancel:
      status === 'draft' ||
      status === 'pending_approval' ||
      status === 'approved' ||
      status === 'rolling' ||
      status === 'observing' ||
      status === 'paused' ||
      status === 'rolling_back',
    rollback:
      status === 'rolling' ||
      status === 'observing' ||
      status === 'paused' ||
      status === 'failed' ||
      status === 'completed' ||
      status === 'rolled_back',
    edit: status === 'draft',
    remove: status === 'draft',
  }
}

/** 状态机可视化用的顺序（跳过 cancelled / rolled_back 等旁路终态）。 */
export const DELIVERY_FLOW: DeliveryStatus[] = [
  'draft',
  'pending_approval',
  'approved',
  'rolling',
  'observing',
  'completed',
]

/**
 * 当前状态在流程条上的位置；侧路状态（paused / failed / rolling_back / rolled_back / cancelled）
 * 返回 -1，由界面单独高亮并说明「已离开主流程」。
 */
export function flowIndexOf(status: DeliveryStatus): number {
  return DELIVERY_FLOW.indexOf(status)
}

/**
 * 失败率与 deferred 的展示口径。
 *
 * 分母 = planned − deferred（§9.1：**分母排除 deferred**），因此这里直接用后端算好的
 * `failureRate`，只在界面上一并显示「挂起 N 台不计入失败率」。
 */
export function deferredNote(measured: Pick<DeliveryObserveMeasured, 'deferred' | 'planned'>): string {
  if (measured.deferred <= 0) return ''
  return `另有 ${measured.deferred} 台离线挂起（deferred），不计入失败率；目标上线后会自动补推`
}
