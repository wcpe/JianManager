import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface ArtifactReconcileRun {
  id: number
  channelId: number
  channelName: string
  status: 'running' | 'succeeded' | 'failed'
  triggeredBy: 'manual' | 'scheduled'
  startedAt: string
  finishedAt?: string
  indexCount: number
  objectCount: number
  matchedCount: number
  missingCount: number
  orphanCount: number
  errorMessage: string
}

export interface ArtifactReconcileDiff {
  id: number
  runId: number
  channelId: number
  kind: 'missing' | 'orphan'
  assetId: number
  sha256: string
  objectKey: string
  size: number
  lastModified?: string
  status: 'open' | 'resolved'
  resolvedAt?: string
  resolvedAction: string
  resolveError: string
}

export interface ArtifactReconcileSettings {
  enabled: boolean
  intervalHours: number
  nextRunAt?: string
}

export interface TriggerReconcileResult {
  started: ArtifactReconcileRun[]
  skipped: Array<{ channelId: number; channelName: string; reason: string }>
}

export interface ReconcileDiffPage {
  items: ArtifactReconcileDiff[]
  total: number
  page: number
  pageSize: number
}

export function useReconcileSettings() {
  return useQuery({
    queryKey: ['artifact-reconcile', 'settings'],
    queryFn: async () => (await api.get<ArtifactReconcileSettings>('/artifact-reconcile/settings')).data,
  })
}

export function useUpdateReconcileSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: { enabled: boolean; intervalHours: number }) =>
      (await api.put<ArtifactReconcileSettings>('/artifact-reconcile/settings', payload)).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-reconcile', 'settings'] }),
  })
}

export function useTriggerReconcile() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (channelId?: number) =>
      (await api.post<TriggerReconcileResult>('/artifact-reconcile/runs', channelId ? { channelId } : {})).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['artifact-reconcile', 'runs'] }),
  })
}

export function useReconcileRuns(limit = 20) {
  return useQuery({
    queryKey: ['artifact-reconcile', 'runs', limit],
    queryFn: async () => (await api.get<ArtifactReconcileRun[]>('/artifact-reconcile/runs', { params: { limit } })).data ?? [],
    refetchInterval: (query) => query.state.data?.some((run) => run.status === 'running') ? 5000 : false,
  })
}

export function useReconcileDiffs(runId: number, kind: 'missing' | 'orphan', page: number, pageSize = 50) {
  return useQuery({
    queryKey: ['artifact-reconcile', 'diffs', runId, kind, page, pageSize],
    enabled: runId > 0,
    queryFn: async () => (await api.get<ReconcileDiffPage>(`/artifact-reconcile/runs/${runId}/diffs`, {
      params: { kind, page, pageSize },
    })).data,
  })
}

export function useResolveMissing() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (runId: number) =>
      (await api.post<{ marked: number; stale: number }>(`/artifact-reconcile/runs/${runId}/resolve-missing`)).data,
    onSuccess: (_data, runId) => {
      qc.invalidateQueries({ queryKey: ['artifact-reconcile', 'diffs', runId] })
      qc.invalidateQueries({ queryKey: ['runtime-assets-overview'] })
    },
  })
}

export function useCleanupOrphans() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (runId: number) =>
      (await api.post<{ cleaned: number; stale: number; failed: number }>(`/artifact-reconcile/runs/${runId}/cleanup-orphans`)).data,
    onSuccess: (_data, runId) => qc.invalidateQueries({ queryKey: ['artifact-reconcile', 'diffs', runId] }),
  })
}
