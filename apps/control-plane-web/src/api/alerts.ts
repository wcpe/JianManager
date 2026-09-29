import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'

/** 告警规则（FR-011 + FR-085）。 */
export interface AlertRuleInfo {
  id: number
  uuid: string
  name: string
  triggerType: string
  level: string
  targetType: string
  targetId: number | null
  metric: string
  operator: string
  threshold: number
  durationSec: number
  keyword: string
  eventMatch: string
  channelIds: string
  dedupWindowSec: number
  silenceStart: string
  silenceEnd: string
  notifyRecover: boolean
  notifyType: string
  notifyTarget: string
  enabled: boolean
  createdAt: string
}

/** 告警事件（FR-011 + FR-085）。 */
export interface AlertEventInfo {
  id: number
  ruleId: number
  targetId: number
  level: string
  triggerType: string
  value: number
  /** 基线/突升突降偏离方向（up|down）；其余触发类型缺省。 */
  direction?: string
  message: string
  count: number
  resolved: boolean
  firedAt: string
  lastFiredAt?: string
  resolvedAt?: string
  acknowledged: boolean
  acknowledgedBy?: number
  acknowledgedAt?: string
  read: boolean
  rule?: { name?: string }
}

/** 通道连接配置（凭证子字段经 ${ENV} 引用）。 */
export interface ChannelConfig {
  url?: string
  token?: string
  chatId?: string
  host?: string
  port?: number
  username?: string
  password?: string
  from?: string
  to?: string
  /** QQ 机器人 AppID（FR-494，明文）。 */
  appId?: string
  /** QQ 机器人密钥（FR-494，须 ${ENV_VAR} 引用）。 */
  appSecret?: string
  /** QQ 投递目标类型：仅 c2c=单聊（FR-494；group 已被平台拒绝，前端不再暴露）。 */
  targetType?: string
  /** QQ 目标 user_openid（单聊）（FR-494，明文）；扫码绑定时自动填入。 */
  targetId?: string
  /** QQ 开放平台 API 根地址（FR-494，可选，留空取后端默认值）。 */
  baseUrl?: string
}

/** 通知通道（FR-085）。 */
export interface AlertChannelInfo {
  id: number
  uuid: string
  name: string
  type: string
  enabled: boolean
  config: string
  createdAt: string
}

/** 创建告警规则请求体。 */
export interface CreateRuleBody {
  name: string
  triggerType: string
  level: string
  targetType: string
  targetId?: number | null
  metric?: string
  operator?: string
  threshold?: number
  durationSec?: number
  keyword?: string
  eventMatch?: string
  channelIds?: number[]
  dedupWindowSec?: number
  silenceStart?: string
  silenceEnd?: string
  notifyRecover?: boolean
  notifyType?: string
  notifyTarget?: string
}

// ── 规则 ──

export function useAlertRules() {
  return useQuery({
    queryKey: ['alertRules'],
    queryFn: async () => {
      const { data } = await api.get<AlertRuleInfo[]>('/alerts/rules')
      return data
    },
  })
}

export function useCreateAlertRule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: CreateRuleBody) => api.post('/alerts/rules', body),
    onSuccess: () => {
      toast.success('告警规则已创建')
      qc.invalidateQueries({ queryKey: ['alertRules'] })
    },
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '创建告警规则失败'),
  })
}

/** 更新告警规则的可变字段（与 CreateRuleBody 不同：触发类型/目标不可改）。 */
export interface UpdateRuleBody {
  id: number
  enabled?: boolean
  threshold?: number
  level?: string
  channelIds?: number[]
  dedupWindowSec?: number
  silenceStart?: string
  silenceEnd?: string
  notifyRecover?: boolean
  keyword?: string
  eventMatch?: string
}

export function useUpdateAlertRule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...body }: UpdateRuleBody) => api.put(`/alerts/rules/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['alertRules'] }),
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '更新失败'),
  })
}

export function useDeleteAlertRule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/alerts/rules/${id}`),
    onSuccess: () => {
      toast.success('告警规则已删除')
      qc.invalidateQueries({ queryKey: ['alertRules'] })
    },
  })
}

// ── 事件 ──

export interface EventQuery {
  ruleId?: number
  resolved?: boolean
  acknowledged?: boolean
  level?: string
  triggerType?: string
  /** 模糊匹配 message（FR-149）。 */
  keyword?: string
  /** fired_at 下界（RFC3339，FR-149）。 */
  from?: string
  /** fired_at 上界（RFC3339，FR-149）。 */
  to?: string
  /** 页码，从 1 起（FR-149）。 */
  page?: number
  /** 每页条数（FR-149，默认后端 50）。 */
  pageSize?: number
}

/** 告警事件分页响应（后端 GET /alerts/events 返回 {items,total}，FR-149）。 */
export interface AlertEventPage {
  items: AlertEventInfo[]
  total: number
}

export function useAlertEvents(params?: EventQuery) {
  return useQuery({
    queryKey: ['alertEvents', params],
    queryFn: async () => {
      const { data } = await api.get<AlertEventPage>('/alerts/events', { params })
      return data
    },
  })
}

export function useUnreadAlertCount() {
  return useQuery({
    queryKey: ['alertUnread'],
    queryFn: async () => {
      const { data } = await api.get<{ unread: number }>('/alerts/events/unread-count')
      return data.unread
    },
    refetchInterval: 30000,
  })
}

export function useAcknowledgeEvent() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/alerts/events/${id}/ack`),
    onSuccess: () => {
      toast.success('告警已确认')
      qc.invalidateQueries({ queryKey: ['alertEvents'] })
      qc.invalidateQueries({ queryKey: ['alertUnread'] })
      qc.invalidateQueries({ queryKey: ['notificationFeed'] })
      qc.invalidateQueries({ queryKey: ['notifications'] })
    },
  })
}

export function useMarkAllRead() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.post('/alerts/events/read-all'),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['alertEvents'] })
      qc.invalidateQueries({ queryKey: ['alertUnread'] })
      qc.invalidateQueries({ queryKey: ['notificationFeed'] })
      qc.invalidateQueries({ queryKey: ['notifications'] })
    },
  })
}

// ── 通道 ──

export function useAlertChannels() {
  return useQuery({
    queryKey: ['alertChannels'],
    queryFn: async () => {
      const { data } = await api.get<AlertChannelInfo[]>('/alerts/channels')
      return data
    },
  })
}

export interface ChannelBody {
  name: string
  type: string
  enabled?: boolean
  config: ChannelConfig
}

export function useCreateAlertChannel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ChannelBody) => api.post('/alerts/channels', body),
    onSuccess: () => {
      toast.success('通道已创建')
      qc.invalidateQueries({ queryKey: ['alertChannels'] })
    },
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '创建通道失败'),
  })
}

export function useUpdateAlertChannel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, ...body }: ChannelBody & { id: number }) =>
      api.put(`/alerts/channels/${id}`, body),
    onSuccess: () => {
      toast.success('通道已更新')
      qc.invalidateQueries({ queryKey: ['alertChannels'] })
    },
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '更新通道失败'),
  })
}

export function useDeleteAlertChannel() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/alerts/channels/${id}`),
    onSuccess: () => {
      toast.success('通道已删除')
      qc.invalidateQueries({ queryKey: ['alertChannels'] })
    },
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '删除失败（可能被规则引用）'),
  })
}

export function useTestAlertChannel() {
  return useMutation({
    mutationFn: (id: number) => api.post(`/alerts/channels/${id}/test`),
    onSuccess: () => toast.success('测试通知已发送'),
    onError: (err: { response?: { data?: { message?: string } } }) =>
      toast.error(err.response?.data?.message || '测试发送失败'),
  })
}

// ── QQ 扫码接入与已发现群（FR-495） ──
// 字段名与后端 QQDiscoveredGroup / QQGatewayConnection 的 JSON tag 对齐（camelCase，
// 与 AlertChannelInfo 等既有接口风格一致）；响应均不含 appSecret（密钥安全验收）。

/** 已发现群（后端 QQDiscoveredGroup 落库 GROUP_ADD_ROBOT 事件）。 */
export interface QQDiscoveredGroup {
  id: number
  groupOpenid: string
  opMemberOpenid: string
  firstSeenAt: string
  lastSeenAt: string
  sourceAppId: string
}

/**
 * 已发现群列表响应：真实后端为分页信封 `{items,total}`（internal/controlplane/router/alert.go
 * QQGroups，devmock 同形状）；仍兼容纯数组，因为故障注入 / 历史 mock 可能给 `[]`
 * （见 normalizeQQDiscoveredGroups）。两种形状都有单测钉住（alerts.test.ts）。
 */
export type QQDiscoveredGroupList = QQDiscoveredGroup[] | { items: QQDiscoveredGroup[]; total: number }

/**
 * 归一化群列表响应为数组（信封取 items，数组原样返回，空/半截响应退化为 `[]`）。
 * 用 `?? []` 是为了半截信封（后端漏 items 字段）不退化成 undefined 而崩在下游 `.map`。
 */
export function normalizeQQDiscoveredGroups(
  data: QQDiscoveredGroupList | undefined | null,
): QQDiscoveredGroup[] {
  if (!data) return []
  return Array.isArray(data) ? data : (data.items ?? [])
}

/**
 * 已发现群列表（enabled=false 时不请求）。
 *
 * 群聊路径已从前端下线（QQ 平台拒绝群主动消息 40034105），故当前无 UI 调用点；保留是因为
 * 后端端点与 `qq_discovered_groups` 表同样保留（平台开放后可即时启用），且响应形状被
 * devmock 契约测试钉住——删掉会让「平台开放时」重新接回失去现成的对齐实现。
 */
export function useQQDiscoveredGroups(enabled = true, refetchInterval: number | false = false) {
  return useQuery({
    queryKey: ['qqDiscoveredGroups'],
    queryFn: async () => {
      const { data } = await api.get<QQDiscoveredGroupList>('/alerts/qq/groups')
      return normalizeQQDiscoveredGroups(data)
    },
    enabled,
    refetchInterval,
  })
}

/** QQ 网关连接状态（后端 QQGatewayConnection：connected/connecting/disconnected/error）。 */
export interface QQGatewayStatusInfo {
  appId: string
  status: string
  lastEventAt?: string | null
  lastError: string
}

/** 网关连接状态（轮询供接入视图实时展示；失败不抛 toast，由视图内联错误态）。 */
export function useQQGatewayStatus(refetchInterval: number | false = false) {
  return useQuery({
    queryKey: ['qqGatewayStatus'],
    queryFn: async () => {
      const { data } = await api.get<QQGatewayStatusInfo>('/alerts/qq/gateway/status')
      return data
    },
    refetchInterval,
  })
}

/**
 * 生成 QQ 机器人分享链接（appId 为空 = 默认机器人；请求不带密钥、响应只有 url）。
 * 用 mutation 而不用 query：每次调用都重新生成，窗口聚焦等自动 refetch 不得静默触发。
 */
export function useCreateQQShareLink() {
  return useMutation({
    mutationFn: async (appId?: string) => {
      const { data } = await api.post<{ url: string }>('/alerts/qq/share-link', {
        appId: appId?.trim() || undefined,
      })
      return data
    },
  })
}

// ── QQ 扫码绑定（FR-494 接入体验改造） ──
// 契约（internal/controlplane/router/alert.go）：
//   POST /alerts/qq/bind-task         → {taskId, qrUrl}；平台侧失败 502 BIND_TASK_FAILED
//   GET  /alerts/qq/bind-task/:taskId → {status}；仅 completed 追加 {appId, userOpenid, secretEnv}
// 响应**绝不含 appSecret 明文**——前端只把 secretEnv（形如 ${QQ-1020001}）填进表单。

/** 绑定任务创建响应：qrUrl 供渲染二维码，打开后跳 QQ 官方连接页。 */
export interface QQBindTaskInfo {
  taskId: string
  qrUrl: string
}

/** 绑定任务状态：none=未开始 / pending=等待扫码 / completed=已授权 / expired=二维码过期。 */
export type QQBindStatus = 'none' | 'pending' | 'completed' | 'expired'

/** 绑定任务轮询结果；appId/userOpenid/secretEnv 只在 completed 时出现。 */
export interface QQBindResult {
  status: QQBindStatus
  appId?: string
  userOpenid?: string
  /** 形如 ${QQ-1020001} 的引用名（密钥已由 CP 落盘，前端只填引用名，不接触明文）。 */
  secretEnv?: string
}

/**
 * 轮询节拍：2s。
 *
 * 上限约束来自平台频率限制（实测分享链接类接口约 9 次即被限流），故节拍不宜更密；
 * 对话框关闭（观察者销毁）后 react-query 会清掉定时器，不会继续打接口。
 */
export const QQ_BIND_POLL_MS = 2000

/** 创建扫码绑定任务（mutation：每次点击都重新申请，不参与缓存与自动重取）。 */
export function useCreateQQBindTask() {
  return useMutation({
    mutationFn: async () => {
      const { data } = await api.post<QQBindTaskInfo>('/alerts/qq/bind-task')
      return data
    },
  })
}

/** 绑定任务本地密钥已过期（后端 404 BIND_TASK_EXPIRED）——提示重扫即可，无需报错。 */
export function isBindTaskExpiredError(err: unknown): boolean {
  const status = (err as { response?: { status?: number } })?.response?.status
  return status === 404
}

/**
 * 查询绑定任务结果（供对话框的轮询循环按 {@link QQ_BIND_POLL_MS} 节流调用）。
 *
 * 不用 react-query 轮询：这轮轮询的判活条件带「过期即重新申请」的副作用，且要暴露
 * 「清定时器」给卸载路径，交给一个显式循环比塞进 refetchInterval 更好读。接口形状与
 * 取消语义（组件卸载后不再请求）仍在组件侧保证。
 */
export async function fetchQQBindResult(taskId: string): Promise<QQBindResult> {
  const { data } = await api.get<QQBindResult>(`/alerts/qq/bind-task/${taskId}`)
  return data
}
