import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

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

export interface ClientDistSecurityListParams {
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
}

export interface BlockIPRequest {
  ip: string
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

const securityKey = ['client-dist-security'] as const

export function useClientDistSecurityOverview() {
  return useQuery({
    queryKey: [...securityKey, 'overview'],
    queryFn: async () => (await api.get<ClientDistSecurityOverview>('/client-dist/security/overview')).data,
    retry: false,
  })
}

export function useClientDistSecurityEvents(params: ClientDistSecurityListParams = {}) {
  return useQuery({
    queryKey: [...securityKey, 'events', params],
    queryFn: async () => (await api.get<ClientDistSecurityEvent[]>('/client-dist/security/events', { params })).data,
    retry: false,
  })
}

export function useClientDistSecurityProfiles(params: ClientDistSecurityListParams = {}) {
  return useQuery({
    queryKey: [...securityKey, 'profiles', params],
    queryFn: async () => (await api.get<ClientDistSecurityProfile[]>('/client-dist/security/profiles', { params })).data,
    retry: false,
  })
}

export function useClientDistSecurityProfile(id: number | null) {
  return useQuery({
    queryKey: [...securityKey, 'profiles', id],
    queryFn: async () => (await api.get<ClientDistSecurityProfileDetail>(`/client-dist/security/profiles/${id}`)).data,
    enabled: id !== null,
    retry: false,
  })
}

export function useClientDistSecurityActions(params: ClientDistSecurityListParams = {}) {
  return useQuery({
    queryKey: [...securityKey, 'actions', params],
    queryFn: async () => (await api.get<ClientProtectionAction[]>('/client-dist/security/actions', { params })).data,
    retry: false,
  })
}

export function useClientDistIpAnalysis(params: ClientDistSecurityListParams = {}) {
  return useQuery({
    queryKey: [...securityKey, 'ip-analysis', params],
    queryFn: async () => (await api.get<ClientDistIpAnalysis[]>('/client-dist/security/ip-analysis', { params })).data,
    retry: false,
  })
}

export function useClientDistPlayerAnalysis(params: ClientDistSecurityListParams = {}) {
  return useQuery({
    queryKey: [...securityKey, 'player-analysis', params],
    queryFn: async () => (await api.get<ClientDistPlayerAnalysis[]>('/client-dist/security/player-analysis', { params })).data,
    retry: false,
  })
}

export function useClientSecurityGroups() {
  return useQuery({
    queryKey: [...securityKey, 'groups'],
    queryFn: async () => (await api.get<ClientSecurityGroup[]>('/client-dist/security/groups')).data,
    retry: false,
  })
}

export function useClientSecurityPrivacyNotice() {
  return useQuery({
    queryKey: [...securityKey, 'privacy-notice'],
    queryFn: async () => (await api.get<ClientSecurityPrivacyNotice>('/client-dist/security/privacy-notice')).data,
    retry: false,
  })
}

export function useBlockClientDistIP() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: BlockIPRequest) => api.post<ClientProtectionAction>('/client-dist/security/ip-blocks', body).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: securityKey }),
  })
}

export function useCancelClientDistIPBlock() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post<ClientProtectionAction>(`/client-dist/security/ip-blocks/${id}/cancel`).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: securityKey }),
  })
}

export function useSetClientDistKeyState() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ keyId, body }: { keyId: string; body: SetKeyStateRequest }) =>
      api.post<ClientProtectionAction>(`/client-dist/security/keys/${keyId}/state`, body).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: securityKey }),
  })
}

export function useSetClientDistChannelProtection() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ channelId, body }: { channelId: string; body: SetChannelProtectionRequest }) =>
      api.put<ClientProtectionAction>(`/client-dist/security/channels/${channelId}/protection`, body).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: securityKey }),
  })
}

export function useClearClientDistChannelProtection() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (channelId: string) => api.delete(`/client-dist/security/channels/${channelId}/protection`),
    onSuccess: () => qc.invalidateQueries({ queryKey: securityKey }),
  })
}

export function useCreateClientSecurityGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: SaveSecurityGroupRequest) => api.post<ClientSecurityGroup>('/client-dist/security/groups', body).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: securityKey }),
  })
}

export function useUpdateClientSecurityGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: SaveSecurityGroupRequest }) =>
      api.put<ClientSecurityGroup>(`/client-dist/security/groups/${id}`, body).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: securityKey }),
  })
}

export function useDeleteClientSecurityGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/client-dist/security/groups/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: securityKey }),
  })
}
