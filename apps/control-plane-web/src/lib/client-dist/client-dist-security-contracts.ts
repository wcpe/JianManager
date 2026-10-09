/**
 * 客户端分发安全契约（FR-430 / ADR-088，ADR-097）。
 * 与后端 model 对齐的纯数据类型，供 `@jianmanager/ui` 内视图逻辑与应用侧 api 共用。
 */

export type SecurityLevel = 'info' | 'warn' | 'high' | 'critical'
export type KeySecurityState = 'normal' | 'observe' | 'throttled' | 'suspended' | 'revoked'
export type ProtectionActionStatus = 'active' | 'expired' | 'canceled'
export type SecurityTargetType = 'ip' | 'key' | 'channel' | 'machine' | 'install' | 'player'
export type ChannelProtectionMode = 'throttle' | 'concurrency' | 'queue' | 'retry_after'

export interface SecurityRankItem {
  subject: string
  count: number
  bytes?: number
  riskScore?: number
}

export interface ClientDistSecurityOverview {
  activeDownloads: number
  downloadBytesPerSecond: number
  abnormalRequests: number
  unauthorizedRequests: number
  forbiddenRequests: number
  rateLimitedRequests: number
  blockedIpCount: number
  throttledKeyCount: number
  protectedChannelCount: number
  topIps: SecurityRankItem[]
  topKeys: SecurityRankItem[]
  topChannels: SecurityRankItem[]
  topPlayers: SecurityRankItem[]
}

export interface ClientDistSecurityEvent {
  id: number
  subjectType: SecurityTargetType
  subjectValue: string
  channelId: string
  machineId: string
  installId: string
  playerName: string
  ip: string
  keyId: number | null
  ruleCode: string
  severity: SecurityLevel
  scoreDelta: number
  action: string
  reason: string
  endpoint: string
  errCode: string
  status: number
  createdAt: string
}

export interface ClientDistSecurityProfile {
  id: number
  channelId: string
  machineId: string
  installId: string
  playerName: string
  keyId: number | null
  keyPrefix: string
  firstSeen: string
  lastSeen: string
  lastIp: string
  userAgent: string
  coreVersion: string
  wedgeVersion: string
  manifestVersion: number
  os: string
  osVersion: string
  arch: string
  javaVendor: string
  javaVersion: string
  javaArch: string
  launcher: string
  locale: string
  timezone: string
  memoryTier: string
  riskScore: number
  riskLevel: SecurityLevel
  protectionState: string
  labels: string[]
  createdAt: string
  updatedAt: string
}

export interface ClientDistSecurityProfileDetail extends ClientDistSecurityProfile {
  recentEvents: ClientDistSecurityEvent[]
  protectionActions: ClientProtectionAction[]
}

export interface ClientChannelSecuritySummary {
  channelId: string
  riskLevel: SecurityLevel
  abnormalRequests: number
  blockedIpCount: number
  restrictedKeyCount: number
  protectionMode: string
  windowMinutes: number
}

export interface ClientDistIpAnalysis {
  ip: string
  requestCount: number
  rejectCount: number
  invalidKeyCount: number
  notFoundCount: number
  rangeCount: number
  downloadBytes: number
  keyCount: number
  channelCount: number
  riskScore: number
  blocked: boolean
  lastSeen: string
}

export interface ClientDistPlayerAnalysis {
  playerName: string
  installCount: number
  machineCount: number
  ipCount: number
  keyCount: number
  channelCount: number
  downloadBytes: number
  abnormalRequests: number
  riskScore: number
  lastSeen: string
}

export interface ClientProtectionAction {
  id: number
  targetType: SecurityTargetType
  targetValue: string
  action: string
  status: ProtectionActionStatus
  policy: Record<string, unknown> | null
  reason: string
  auto: boolean
  expiresAt: string | null
  createdBy: number
  createdAt: string
  updatedAt: string
}

export interface ClientSecurityGroup {
  id: number
  name: string
  kind: 'manual' | 'dynamic'
  targetType: SecurityTargetType
  rule: Record<string, unknown> | null
  actionPolicy: Record<string, unknown> | null
  enabled: boolean
  createdBy: number
  createdAt: string
  updatedAt: string
}

export interface ClientSecurityPrivacyNotice {
  requiredFields: string[]
  diagnosticFields: string[]
  notice: string
  retentionDays: number
}

export type ClientDistSecurityLogType = 'hello' | 'risk' | 'action' | 'request' | 'runtime' | 'telemetry'

export interface ClientDistSecurityLogItem {
  id: string
  type: ClientDistSecurityLogType
  title: string
  channelId?: string
  machineId?: string
  playerName?: string
  ip?: string
  status?: string
  errCode?: string
  createdAt: string
  detail: Record<string, unknown> | null
}

export interface ClientDistSecurityLogPage {
  items: ClientDistSecurityLogItem[]
  total: number
  page: number
  pageSize: number
}

export interface ClientDistSecurityListParams {
  type?: ClientDistSecurityLogType | 'all'
  channelId?: string
  ip?: string
  keyId?: string | number
  machineId?: string
  installId?: string
  playerName?: string
  endpoint?: string
  errCode?: string
  riskRule?: string
  limit?: number
  page?: number
  pageSize?: number
  /** 处置流水服务端筛选（S2.5）。 */
  targetType?: SecurityTargetType
  status?: ProtectionActionStatus
  q?: string
}

export interface BlockIPRequest {
  ip: string
  channelId?: string
  reason: string
  durationMinutes: number
}

export interface SetKeyStateRequest {
  state: KeySecurityState
  reason: string
  throttlePolicy?: Record<string, unknown>
}

export interface SetChannelProtectionRequest {
  mode: ChannelProtectionMode
  reason: string
  retryAfterSeconds?: number
  maxConcurrency?: number
  rateLimitPerSecond?: number
}

export interface SaveSecurityGroupRequest {
  name: string
  kind: 'manual' | 'dynamic'
  targetType: SecurityTargetType
  rule?: Record<string, unknown> | null
  actionPolicy?: Record<string, unknown> | null
  enabled: boolean
}
