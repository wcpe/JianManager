/**
 * 告警域契约（FR-085 / FR-494，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

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

/** 扫码结果回填载荷：appSecret 位置放的是后端返回的**引用名**（secretEnv），不是明文密钥。 */
export interface QQBindFill {
  appId: string
  /** 形如 ${QQ-102000001} 的 ${ENV} 引用名（密钥已由 CP 落盘，前端不接触明文）。 */
  secretEnv: string
  /** 单聊目标 user_openid。 */
  userOpenid: string
}

/** 通道创建/更新请求体（FR-085；不含 id，编辑时由调用方补）。 */
export interface ChannelSubmitBody {
  name: string
  type: string
  enabled: boolean
  config: ChannelConfig
}

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
  /**
   * 实例展示名（后端按规则维度回填）：仅当规则的 targetType 为 instance 时存在。
   * 有了它，列表就不必为了显示名字而拉全量实例列表。实例被删时缺省，前端回退显示 #id。
   */
  instanceName?: string
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

/** 规则提交载荷：创建与更新的字段集不同（编辑时触发类型/目标不可改），用 mode 区分。 */
export type RuleSubmitPayload =
  | { mode: 'create'; body: CreateRuleBody }
  | { mode: 'update'; body: UpdateRuleBody }
