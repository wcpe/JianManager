import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { apiErrorMessage } from '@/lib/shared/api-error'

/**
 * 探针托管配置：三层增量、下发编排与逐台目标。
 *
 * 契约真源是后端 `internal/controlplane/service/managed_config.go` /
 * `managed_push.go` 与 `internal/controlplane/router/managed_config.go`，
 * 本文件只做类型镜像与请求封装，不复制任何业务判定。
 *
 * 【三条必须由界面诚实表达的语义】
 * 1. `FORBIDDEN` / 档位越界是**整体拒绝**，不是「跳过那个键」。运维必须看到具体是哪个键，
 *    所以错误文案一律原样展示后端 message（它带着键名），不做二次包装。
 * 2. 保存是**版本乐观并发**：`expectedVersion` 是「我改的是我以为的那一版」，
 *    409 VERSION_CONFLICT 必须提示重新加载，而不是静默重试覆盖别人的修改。
 * 3. 下发会话不会自己开始，也不会自己放行下一批：`awaiting_release` 是「等人点继续」的
 *    显式状态，与 `running`（正在推）必须分开呈现。
 */

// ---------------------------------------------------------------------------
// 枚举（与 internal/controlplane/model/managed_config.go 逐值对应）
// ---------------------------------------------------------------------------

/** 托管配置作用域（低 → 高优先级：global → node → instance）。 */
export type ManagedScope = 'global' | 'node' | 'instance'

/** 下发会话状态。`awaiting_release` 是「本批已终结、等人工放行」。 */
export type ManagedPushStatus =
  | 'running'
  | 'awaiting_release'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'cancelled'

/** 单台目标状态。`timeout` 是「没回执」，与探针明确回报的 `failed` 语义相反。 */
export type ManagedTargetState =
  | 'pending'
  | 'pushing'
  | 'applied'
  | 'failed'
  | 'rejected'
  | 'timeout'
  | 'skipped'

/** 一致性判定。 */
export type ManagedConsistency = 'inSync' | 'drift' | 'neverReported' | 'failed'

/** 合成后的连接状态（不是探针自报值，见后端 SynthesizeManagedConnection）。 */
export type ManagedConnection = 'connected' | 'degraded' | 'unauthorized' | 'disconnected'

/** 配置来源：file = 探针按本地 config.yml 跑；cp = 已由 CP 下发过。 */
export type ManagedConfiguration = 'file' | 'cp'

// ---------------------------------------------------------------------------
// 实体
// ---------------------------------------------------------------------------

/** 一层托管配置的增量（不是合并后的全量）。 */
export interface ManagedConfigLayer {
  id: number
  scope: ManagedScope
  /** global=0、node=nodes.id、instance=instances.id。 */
  scopeRefId: number
  /** 作用域可读名（节点名 / 实例名；global 为空）。 */
  scopeName?: string
  /** 增量配置 JSON 文本（嵌套对象；显式 null 表示删键）。 */
  config: string
  /** 该层写入代数（每次保存 +1）。 */
  version: number
  remark?: string
  createdBy: number
  createdAt: string
  updatedAt: string
}

/** 保存一层的请求体。 */
export interface ManagedLayerInput {
  scope: ManagedScope
  scopeRefId: number
  /** 增量 JSON 文本；空串 = 清空该层。 */
  config: string
  /** 乐观并发基线：0 表示「我认为这层还不存在」。 */
  expectedVersion: number
  /** 备注（进审计）。 */
  remark?: string
}

/** 一个键的最终值与其来源（逐键下钻用）。 */
export interface ManagedTracedKey {
  key: string
  value: unknown
  scope: ManagedScope
  scopeRefId: number
  scopeName?: string
  version: number
  /** true 表示该键被更高层的显式 null 删除。 */
  deleted?: boolean
  /** 档位（safe / guarded / forbidden 的机器名由后端给出）。 */
  tier: string
  tierKnown: boolean
  tierForbidden?: boolean
}

/** 影响预演结果。 */
export interface ManagedImpact {
  /** 受影响的（探针适用）实例数。 */
  affectedInstances: number
  /** 该作用域下的实例总数（含探针不适用者，便于解释差额）。 */
  totalInstances: number
  /** 触及危险档的键（改变数据量 / 资源占用）。 */
  guardedKeys: string[]
  /** 越界键：非空即不可下发。 */
  forbiddenKeys: string[]
  /** 指标量倍率（默认周期 / 生效周期）。 */
  metricMultiplier: number
  defaultCollectPeriodTicks: number
  effectiveCollectPeriodTicks: number
  /** 后端给出的人读说明（原样展示）。 */
  notes: string[]
}

/**
 * 校验结果（后端 `ManagedConfigPushValidation`，字段名 Errors → JSON `Errors`）。
 *
 * 注意：该结构没有 json tag，Go 序列化用的是字段名本身。故这里字段取 `Errors`。
 */
export interface ManagedValidateResult {
  /** 逐键拒绝原因（已排序）。空数组即通过。 */
  Errors?: string[]
  /** 部分后端版本可能用此形态返回同样的清单。 */
  errors?: string[]
}

/** 下发会话。 */
export interface ManagedPushSession {
  id: number
  kind: 'push' | 'rollback'
  scope: ManagedScope
  scopeRefId: number
  scopeName?: string
  configVersion: number
  reason?: string
  operator?: string
  batchSize: number
  failureRateThreshold: number
  ackTimeoutSec: number
  status: ManagedPushStatus
  pauseKind?: string
  pauseReason?: string
  breakReason?: string
  breakThreshold?: number
  breakObserved?: number
  plannedCount: number
  appliedCount: number
  failedCount: number
  timeoutCount: number
  skippedCount: number
  pendingCount: number
  currentBatch: number
  totalBatches: number
  sourceSessionId?: number
  createdBy: number
  createdAt: string
  updatedAt: string
  startedAt?: string | null
  finishedAt?: string | null
}

/** 逐台目标（列表一行）。 */
export interface ManagedPushTarget {
  id: number
  sessionId: number
  instanceId: number
  instanceUuid?: string
  instanceName: string
  nodeId: number
  batchNo: number
  state: ManagedTargetState
  desiredVersion: number
  baseVersion: number
  ackVersion?: number | null
  /** 探针回报的生效值（JSON 文本，可展开查看）。 */
  effectiveJson?: string
  errorKind?: string
  error?: string
  attempts: number
  pushedAt?: string | null
  finishedAt?: string | null
  createdAt: string
  updatedAt: string
}

/** 建会话的请求体。 */
export interface ManagedPushInput {
  scope: ManagedScope
  scopeRefId: number
  /** 显式目标；为空则按作用域自动展开。 */
  instanceIds?: number[]
  /** 每批台数；0 = 单批全量。 */
  batchSize?: number
  /** 失败率熔断阈值（%）；省略用默认 20，0 = 关闭熔断。 */
  failureRateThreshold?: number | null
  /** ACK 超时（秒）；0 用默认 30。 */
  ackTimeoutSec?: number
  reason?: string
}

/** 回滚请求体。`confirmed` 必须为 true（后端拒绝静默回滚）。 */
export interface ManagedRollbackInput {
  sourceSessionId: number
  /** 子集；空 = 整批。 */
  instanceIds?: number[]
  reason: string
  confirmed: boolean
}

/** 分页信封（后端统一 `{ items, total, page, pageSize }`）。 */
export interface ManagedPage<T> {
  items: T[]
  total: number
  page: number
  pageSize: number
}

/** 取某层的响应：404 时表示尚未配置。 */
export type ManagedLayerResult = { found: true; layer: ManagedConfigLayer } | { found: false }

// ---------------------------------------------------------------------------
// 纯函数（界面与测试共用）
// ---------------------------------------------------------------------------

/** 统一取校验结果里的错误清单（兼容两种字段名）。 */
export function validateErrors(result: ManagedValidateResult | undefined): string[] {
  if (!result) return []
  return result.Errors ?? result.errors ?? []
}

/** 会话是否已终结。 */
export function isPushTerminal(status: ManagedPushStatus): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled'
}

/**
 * 放行下一批的前置条件。
 *
 * 判据全部取自后端 `Release` 的两条显式校验（状态必须是 awaiting_release、
 * 且不能已是最后一批）。前端据此禁用按钮并给出原因，避免用户点下去只拿到 409。
 */
export interface ReleaseGuard {
  allowed: boolean
  /** 不允许时的原因（i18n 键后缀），允许时为空串。 */
  reasonKey: '' | 'releaseNotAwaiting' | 'releaseLastBatch'
}

/** 计算放行按钮的可点性与原因。 */
export function releaseGuard(session: Pick<ManagedPushSession, 'status' | 'currentBatch' | 'totalBatches'>): ReleaseGuard {
  if (session.status !== 'awaiting_release') return { allowed: false, reasonKey: 'releaseNotAwaiting' }
  if (session.currentBatch >= session.totalBatches) return { allowed: false, reasonKey: 'releaseLastBatch' }
  return { allowed: true, reasonKey: '' }
}

/** 会话状态 → 状态色等级。 */
export function pushStatusLevel(status: ManagedPushStatus): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  switch (status) {
    case 'completed':
      return 'success'
    // 运行中与「等人放行」必须不同色：后者不是进度条在动，而是需要人来点。
    case 'running':
      return 'info'
    case 'awaiting_release':
      return 'warning'
    case 'paused':
      return 'warning'
    case 'failed':
      return 'danger'
    case 'cancelled':
      return 'neutral'
    default:
      return 'neutral'
  }
}

/** 目标状态 → 状态色等级：`timeout` 是「不知道」，与明确失败的 `failed` 同级但不同文案。 */
export function targetStateLevel(state: ManagedTargetState): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  switch (state) {
    case 'applied':
      return 'success'
    case 'pushing':
      return 'info'
    case 'pending':
      return 'neutral'
    case 'failed':
    case 'rejected':
    case 'timeout':
      return 'danger'
    case 'skipped':
      return 'warning'
    default:
      return 'neutral'
  }
}

/** 目标是否属于失败类（**不含** skipped：离线跳过的机器不该算「配置推坏了」）。 */
export function isTargetFailure(state: ManagedTargetState): boolean {
  return state === 'failed' || state === 'rejected' || state === 'timeout'
}

/** 会话状态的可选项顺序（按生命周期排，不是字母序）。 */
export const MANAGED_PUSH_STATUSES: ManagedPushStatus[] = [
  'running',
  'awaiting_release',
  'paused',
  'completed',
  'failed',
  'cancelled',
]

/** 目标状态的可选项顺序。 */
export const MANAGED_TARGET_STATES: ManagedTargetState[] = [
  'pending',
  'pushing',
  'applied',
  'failed',
  'rejected',
  'timeout',
  'skipped',
]

/** 解析目标 `effectiveJson`（后端以 JSON 文本承载，非对象一律返回 null）。 */
export function parseEffectiveJson(raw: string | undefined): unknown {
  if (!raw) return null
  try {
    return JSON.parse(raw) as unknown
  } catch {
    // 后端存的是上一轮的原文；解析失败原样回显比空白更可解释。
    return raw
  }
}

/** 把任意对象格式化为可读 JSON（空值给空串，调用方决定占位符）。 */
export function prettyJson(value: unknown): string {
  if (value === null || value === undefined) return ''
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

/** 三层配置层列表（GET /managed-configs，scope 省略 = 三层全取）。 */
export function useManagedLayers(scope?: ManagedScope) {
  return useQuery({
    queryKey: ['managed-configs', scope ?? 'all'],
    queryFn: async () => {
      const { data } = await api.get<{ items: ManagedConfigLayer[] }>('/managed-configs', {
        params: scope ? { scope } : {},
      })
      return data.items ?? []
    },
  })
}

/**
 * 取某一层（GET /managed-configs/layer）。
 *
 * 404 = 该层尚未配置，**不是错误**：界面上这是「空草稿 + 版本 0」的合法起点。
 * 故把 404 归一成 `{ found: false }`，其余错误照常抛出。
 */
export function useManagedLayer(scope: ManagedScope, scopeRefId: number) {
  return useQuery({
    queryKey: ['managed-configs', 'layer', scope, scopeRefId],
    queryFn: async (): Promise<ManagedLayerResult> => {
      try {
        const { data } = await api.get<ManagedConfigLayer>('/managed-configs/layer', {
          params: { scope, scopeRefId },
        })
        return { found: true, layer: data }
      } catch (err) {
        const status = (err as { response?: { status?: number } }).response?.status
        if (status === 404) return { found: false }
        throw err
      }
    },
    // instance 层必须选到具体实例才有意义；node 层同理。避免 scopeRefId=0 时打无效请求。
    enabled: scope === 'global' || scopeRefId > 0,
  })
}

/** 会话分页列表（GET /managed-pushes — 后端列表端点见路由器注册）。 */
export function useManagedPushes(params: { status?: string; q?: string; page?: number; pageSize?: number } = {}) {
  return useQuery({
    queryKey: ['managed-pushes', params],
    queryFn: async () => {
      const { data } = await api.get<ManagedPage<ManagedPushSession>>('/managed-pushes', { params })
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

/** 逐台目标分页（POST /managed-pushes/:id/targets —— 注意是 POST + 查询串）。 */
export function useManagedPushTargets(
  sessionId: number | null,
  params: { state?: string; batchNo?: number; page?: number; pageSize?: number } = {},
  live = false,
) {
  return useQuery({
    queryKey: ['managed-pushes', sessionId, 'targets', params],
    queryFn: async () => {
      const { data } = await api.post<ManagedPage<ManagedPushTarget>>(
        `/managed-pushes/${sessionId}/targets`,
        null,
        { params },
      )
      return {
        items: data.items ?? [],
        total: data.total ?? 0,
        page: data.page ?? 1,
        pageSize: data.pageSize ?? 20,
      }
    },
    enabled: !!sessionId,
    placeholderData: keepPreviousData,
    // 推进中的会话目标状态持续变化：不轮询的话「等待回执 → 已应用」永远停在旧值。
    refetchInterval: live ? 3000 : false,
    refetchIntervalInBackground: false,
  })
}

// ---------------------------------------------------------------------------
// 写动作
// ---------------------------------------------------------------------------

/**
 * 保存一层（PUT /managed-configs）。
 *
 * 刻意**不做乐观更新**：保存后版本会自增，本地猜测的版本号一旦错了，
 * 下一次保存就会拿着错误的 `expectedVersion` 撞 409。让后端返回的行成为唯一真源。
 */
export function useSaveManagedLayer() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: ManagedLayerInput) => {
      const { data } = await api.put<ManagedConfigLayer>('/managed-configs', input)
      return data
    },
    onSuccess: () => {
      // 不能整体失效 'managed-configs'：前缀匹配会连带层详情查询
      // ['managed-configs','layer',...] 一起重取，版本号变化会让编辑器重挂、
      // 其它键的未落盘草稿随旧实例销毁（单键写入 keepDraft 路径就靠不重取层
      // 保草稿）。这里只刷列表与目录；层详情由调用方按需显式 refetch——
      // 整层保存在 onSaved 里做，单键写入刻意跳过。
      void qc.invalidateQueries({
        predicate: (q) => q.queryKey[0] === 'managed-configs' && q.queryKey[1] !== 'layer',
      })
      toast.success('配置层已保存')
    },
    onError: (err) => {
      // 冲突文案必须与其它失败区分：它要求用户「重新加载」，不是「重试」。
      const status = (err as { response?: { status?: number } }).response?.status
      if (status === 409) {
        toast.error(apiErrorMessage(err, '该层已被他人更新，请重新加载'))
        return
      }
      toast.error(apiErrorMessage(err, '保存配置层失败'))
    },
  })
}

/** 删除一层（DELETE /managed-configs?scope=&scopeRefId=）。 */
export function useDeleteManagedLayer() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ scope, scopeRefId }: { scope: ManagedScope; scopeRefId: number }) => {
      await api.delete('/managed-configs', { params: { scope, scopeRefId } })
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['managed-configs'] })
      toast.success('配置层已删除')
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, '删除配置层失败'))
    },
  })
}

/**
 * 实时校验（POST /managed-configs/validate）。
 *
 * 用 mutation 而不是 query：它由「文本变化 → 消抖后触发」驱动，且同一段文本会被反复校验；
 * query 的缓存键要序列化整段 JSON，收益远小于成本。
 */
export function useValidateManagedConfig() {
  return useMutation({
    mutationFn: async (input: { scope: ManagedScope; scopeRefId: number; config: string }) => {
      const { data } = await api.post<ManagedValidateResult>('/managed-configs/validate', input)
      return data
    },
    // 校验失败是常态（用户正在编辑），不在这一层弹 toast。
  })
}

/** 影响预演（POST /managed-configs/impact）。 */
export function useManagedImpact() {
  return useMutation({
    mutationFn: async (input: { scope: ManagedScope; scopeRefId: number; config: string }) => {
      const { data } = await api.post<ManagedImpact>('/managed-configs/impact', input)
      return data
    },
  })
}

/** 建下发会话（POST /managed-pushes）。不自动开始——后端返回后由调用方决定是否 start。 */
export function useCreateManagedPush() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: ManagedPushInput) => {
      const { data } = await api.post<ManagedPushSession>('/managed-pushes', input)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['managed-pushes'] })
      toast.success('下发会话已创建')
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, '创建下发会话失败'))
    },
  })
}

/** 会话动作名（推进类）。 */
type PushActionName = 'start' | 'release' | 'pause' | 'resume' | 'cancel'

/** 逐个动作的成功文案。 */
const PUSH_ACTION_SUCCESS: Record<PushActionName, string> = {
  start: '已开始推送第一批',
  release: '已放行下一批',
  pause: '已暂停推进（未回滚已生效目标）',
  resume: '已恢复推进',
  cancel: '会话已取消（已生效目标未回滚）',
}

/** 逐个动作的兜底失败文案。 */
const PUSH_ACTION_FALLBACK: Record<PushActionName, string> = {
  start: '开始推送失败',
  release: '放行下一批失败',
  pause: '暂停失败',
  resume: '恢复失败',
  cancel: '取消失败',
}

/**
 * 动作 mutation 的公共工厂。
 *
 * `TResult` 由各自的 `run` 推断：`release/pause/resume/cancel` 返回后端给的完整会话，
 * 而 `start` 只返回 `{started:true}`（没有会话），调用方据此决定是「用返回值刷新」
 * 还是「本地改状态」。把这个差异保留在类型里，比统一返回 `unknown` 再强转安全。
 */
function usePushAction<TVars, TResult = ManagedPushSession>(
  action: PushActionName,
  run: (vars: TVars) => Promise<TResult>,
) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: run,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['managed-pushes'] })
      void qc.invalidateQueries({ queryKey: ['managed-matrix'] })
      toast.success(PUSH_ACTION_SUCCESS[action])
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, PUSH_ACTION_FALLBACK[action]))
    },
  })
}

/** 开始推第一批（POST /managed-pushes/:id/start）。后端只回 `{started:true}`，不回会话。 */
export function useStartManagedPush() {
  return usePushAction<number, void>('start', async (id) => {
    await api.post(`/managed-pushes/${id}/start`)
  })
}

/** 放行下一批（POST /managed-pushes/:id/release）。 */
export function useReleaseManagedPush() {
  return usePushAction<number>('release', async (id) => {
    const { data } = await api.post<ManagedPushSession>(`/managed-pushes/${id}/release`)
    return data
  })
}

/** 暂停（POST /managed-pushes/:id/pause，body `{ reason }`）。 */
export function usePauseManagedPush() {
  return usePushAction<{ id: number; reason?: string }>('pause', async ({ id, reason }) => {
    const { data } = await api.post<ManagedPushSession>(`/managed-pushes/${id}/pause`, { reason })
    return data
  })
}

/** 恢复（POST /managed-pushes/:id/resume）。 */
export function useResumeManagedPush() {
  return usePushAction<number>('resume', async (id) => {
    const { data } = await api.post<ManagedPushSession>(`/managed-pushes/${id}/resume`)
    return data
  })
}

/** 取消（POST /managed-pushes/:id/cancel，body `{ reason }`）。 */
export function useCancelManagedPush() {
  return usePushAction<{ id: number; reason?: string }>('cancel', async ({ id, reason }) => {
    const { data } = await api.post<ManagedPushSession>(`/managed-pushes/${id}/cancel`, { reason })
    return data
  })
}

/**
 * 回滚（POST /managed-pushes/rollback）。
 *
 * 语义是「重推上一次 completed 会话的**冻结快照**」——不是「按现在的层重新合并一遍」。
 * 后端要求 `confirmed=true` 且原因非空，二者缺一即拒；界面侧同样不允许空原因提交。
 */
export function useManagedRollback() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (input: ManagedRollbackInput) => {
      const { data } = await api.post<ManagedPushSession>('/managed-pushes/rollback', input)
      return data
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['managed-pushes'] })
      void qc.invalidateQueries({ queryKey: ['managed-matrix'] })
      toast.success('回滚会话已创建')
    },
    onError: (err) => {
      toast.error(apiErrorMessage(err, '回滚失败'))
    },
  })
}

// ===========================================================================
// 配置键目录（可视化编辑器的数据源）
// ===========================================================================

/** 配置键的编辑类型，决定前端渲染哪种控件。 */
export type ManagedKeyKind = 'bool' | 'int' | 'float' | 'string' | 'stringList' | 'object'

/** 配置键的界面分组。 */
export type ManagedKeyGroup =
  | 'collect'
  | 'history'
  | 'world'
  | 'folia'
  | 'forensic'
  | 'http'
  | 'cpu'
  | 'incision'
  | 'alert'
  | 'mcp'
  | 'managed'
  | 'local'

/** 一个配置键的编辑元数据（后端 managedKeyCatalog 的投影）。 */
export interface ManagedKeyMeta {
  key: string
  kind: ManagedKeyKind
  group: ManagedKeyGroup
  /** 档位：safe / guarded / forbidden。禁止档只能看不能改。 */
  tier: string
  title: string
  description: string
  default?: unknown
  min?: number
  max?: number
  boundsHint?: string
  options?: string[]
  unit?: string
  /** 仅 object 类型：子字段结构（如 alert.rules 的单条规则）。 */
  fields?: ManagedKeyMeta[]
}

export interface ManagedKeyCatalogResponse {
  items: ManagedKeyMeta[]
  /** 分组 → 中文名（服务端维护，前端不另存一份）。 */
  groups: Record<string, string>
}

/**
 * 配置键目录（GET /managed-configs/catalog）。
 *
 * 这份数据是全量可视化编辑的基础：没有它，前端只能给一个 JSON 文本框。
 * 目录是编译期常量，不会变，故 staleTime 设长、不重取。
 */
export function useManagedKeyCatalog() {
  return useQuery({
    queryKey: ['managed-configs', 'catalog'],
    queryFn: async () => {
      const { data } = await api.get<ManagedKeyCatalogResponse>('/managed-configs/catalog')
      return data
    },
    staleTime: 30 * 60 * 1000,
  })
}

/** 判定某键是否禁止远程写入（只能看不能改）。 */
export function isForbiddenTier(meta: ManagedKeyMeta): boolean {
  return meta.tier === 'forbidden'
}

/**
 * 把配置对象摊平成「点分键 → 值」。
 *
 * 与后端合并口径一致：只在**扁平点分键**上操作，`alert.rules.tps-warn.threshold`
 * 是一个键而不是嵌套结构。可视化编辑器按扁平键渲染，提交时再组装回嵌套对象。
 *
 * 例外：`alert.rules` 是 object 类型（值是「规则名 → 规则体」的映射），
 * 它作为**一个整体**保留，不再按 `.` 继续摊平——否则规则名会被当成配置键。
 */
export function flattenManagedConfig(
  doc: Record<string, unknown>,
  objectKeys: Set<string> = new Set(['alert.rules']),
): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  const walk = (prefix: string, value: unknown) => {
    if (prefix && objectKeys.has(prefix)) {
      out[prefix] = value
      return
    }
    if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
      const entries = Object.entries(value as Record<string, unknown>)
      if (entries.length === 0) {
        out[prefix] = value
        return
      }
      for (const [k, v] of entries) {
        walk(prefix ? `${prefix}.${k}` : k, v)
      }
      return
    }
    out[prefix] = value
  }
  for (const [k, v] of Object.entries(doc)) {
    walk(k, v)
  }
  return out
}

/**
 * 把扁平点分键还原成嵌套对象（提交给后端的形态）。
 *
 * `alert.rules` 这类 object 键的值原样保留，不再拆解。
 */
export function unflattenManagedConfig(
  flat: Record<string, unknown>,
  objectKeys: Set<string> = new Set(['alert.rules']),
): Record<string, unknown> {
  const root: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(flat)) {
    if (objectKeys.has(key)) {
      root[key] = value
      continue
    }
    const parts = key.split('.')
    let cursor = root
    for (let i = 0; i < parts.length - 1; i += 1) {
      const seg = parts[i]!
      const next = cursor[seg]
      if (next === null || typeof next !== 'object' || Array.isArray(next)) {
        cursor[seg] = {}
      }
      cursor = cursor[seg] as Record<string, unknown>
    }
    cursor[parts[parts.length - 1]!] = value
  }
  return root
}
