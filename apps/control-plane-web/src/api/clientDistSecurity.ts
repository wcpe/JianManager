import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
/**
 * 客户端分发安全契约（FR-430 / ADR-088）已回迁应用侧，双侧共用（ADR-097）；对外保留原导出名，调用点无需改动。
 */
export type { SecurityLevel, KeySecurityState, ProtectionActionStatus, SecurityTargetType, ChannelProtectionMode, SecurityRankItem, ClientDistSecurityOverview, ClientDistSecurityEvent, ClientDistSecurityProfile, ClientDistSecurityProfileDetail, ClientChannelSecuritySummary, ClientDistIpAnalysis, ClientDistPlayerAnalysis, ClientProtectionAction, ClientSecurityGroup, ClientSecurityPrivacyNotice, ClientDistSecurityLogType, ClientDistSecurityLogItem, ClientDistSecurityLogPage, ClientDistSecurityListParams, BlockIPRequest, SetKeyStateRequest, SetChannelProtectionRequest, SaveSecurityGroupRequest } from '@/lib/client-dist-security-contracts'
import type { ClientDistSecurityOverview, ClientDistSecurityEvent, ClientDistSecurityProfile, ClientDistSecurityProfileDetail, ClientChannelSecuritySummary, ClientDistIpAnalysis, ClientDistPlayerAnalysis, ClientProtectionAction, ClientSecurityGroup, ClientSecurityPrivacyNotice, ClientDistSecurityLogPage, ClientDistSecurityListParams, BlockIPRequest, SetKeyStateRequest, SetChannelProtectionRequest, SaveSecurityGroupRequest } from '@/lib/client-dist-security-contracts'

const securityKey = ['client-dist-security'] as const

export function useClientDistSecurityOverview() {
  return useQuery({
    queryKey: [...securityKey, 'overview'],
    queryFn: async () => (await api.get<ClientDistSecurityOverview>('/client-dist/security/overview')).data,
    retry: false,
  })
}

export function useClientChannelSecuritySummary(channelId: string) {
  return useQuery({
    queryKey: [...securityKey, 'channel-summary', channelId],
    queryFn: async () => (await api.get<ClientChannelSecuritySummary>(`/client-channels/${channelId}/security-summary`)).data,
    enabled: channelId !== '',
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

export function useClientDistSecurityLogs(params: ClientDistSecurityListParams = {}) {
  const queryParams = { ...params, type: params.type === 'all' ? undefined : params.type }
  return useQuery({
    queryKey: [...securityKey, 'logs', params],
    queryFn: async () => (await api.get<ClientDistSecurityLogPage>('/client-dist/security/logs', { params: queryParams })).data,
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
