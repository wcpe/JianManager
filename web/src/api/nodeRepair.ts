import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import type { NodeInfo } from '@/api/nodes'

/**
 * 坏节点修复 API（BUG-A / ADR-039 §2）：诊断疑似被串改/重名的节点，并提供破坏性修复
 * （重新 enroll 轮换身份 / 清理孤立 JDK 与实例）。后端已带二次确认（confirm=true）+ 审计，
 * 仅平台管理员可用；功能未开启时端点回 404，调用方应优雅降级。
 */

/** 一条疑似坏节点诊断记录（GET /nodes/repair/suspects 的元素）。 */
export interface SuspectNode {
  /** 疑似节点（字段同节点列表项）。 */
  node: NodeInfo
  /** 命中的可疑信号说明（中文，后端给出）。 */
  reasons: string[]
}

/** 某节点孤立资源统计（GET /nodes/:id/orphans）。 */
export interface OrphanReport {
  nodeId: number
  jdkCount: number
  instanceCount: number
}

/** 重新 enroll 结果（POST /nodes/:id/reenroll）：新身份一次性随响应返回。 */
export interface ReenrollResult {
  nodeId: number
  newUuid: string
  /** 新节点密钥，仅此一次随响应返回，需复制保存。 */
  newSecret: string
  oldUuid: string
}

/** 孤儿清理结果（POST /nodes/:id/purge-orphans）。 */
export interface PurgeOrphansResult {
  nodeId: number
  jdkDeleted: number
  instancesPurged: number
}

/** 列出疑似坏节点（只读诊断，仅平台管理员）。功能未开启（404）由调用方降级处理。 */
export function useNodeSuspects(options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['node-repair', 'suspects'],
    queryFn: async () => {
      const { data } = await api.get<SuspectNode[]>('/nodes/repair/suspects')
      return data
    },
    enabled: options?.enabled ?? true,
    retry: false,
  })
}

/** 统计某节点孤立 JDK/实例数量（只读，修复前评估影响面）。 */
export function useNodeOrphans(nodeId: number, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['node-repair', 'orphans', nodeId],
    queryFn: async () => {
      const { data } = await api.get<OrphanReport>(`/nodes/${nodeId}/orphans`)
      return data
    },
    enabled: (options?.enabled ?? true) && !!nodeId,
    retry: false,
  })
}

/** 重新 enroll：为被挤占机器轮换全新 UUID/secret（破坏性，需 confirm=true）。 */
export function useReenrollNode(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const { data } = await api.post<ReenrollResult>(`/nodes/${nodeId}/reenroll`, { confirm: true })
      return data
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      qc.invalidateQueries({ queryKey: ['node-repair'] })
    },
  })
}

/** 清理某节点孤立 JDK/实例引用（破坏性，需 confirm=true）。 */
export function usePurgeOrphans(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const { data } = await api.post<PurgeOrphansResult>(`/nodes/${nodeId}/purge-orphans`, { confirm: true })
      return data
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      qc.invalidateQueries({ queryKey: ['node-repair'] })
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })
}
