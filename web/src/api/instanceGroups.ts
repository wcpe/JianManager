import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 实例组织分组树节点（对应后端 service.InstanceGroupNodeView，FR-165 / ADR-XXXX）。
 * 扁平节点列表，前端据 parentId 重建层级；instanceCount 为「子树聚合去重」实例数。
 * 与用户组（RBAC）、网络群组（部署）正交——纯组织归类。
 */
export interface InstanceGroupNode {
  id: number
  uuid: string
  name: string
  /** 父节点 ID，null=根分组。 */
  parentId: number | null
  sort: number
  /** 子树（含自身及所有后代）去重后的实例数。 */
  instanceCount: number
}

/** 分组成员实例概要。 */
export interface InstanceGroupMember {
  instanceId: number
  name: string
  role: string
  nodeId: number
  status: string
}

/** 分组树（扁平节点列表）。 */
export function useInstanceGroups() {
  return useQuery({
    queryKey: ['instanceGroups'],
    queryFn: async () => {
      const { data } = await api.get<InstanceGroupNode[]>('/instance-groups')
      return data
    },
  })
}

/**
 * 某分组「子树（含自身及后代）去重」的实例 ID 集合，供「按组筛选」与右列表共用。
 * 仅在选中某组时启用（groupId>0）。
 */
export function useInstanceGroupSubtree(groupId: number | null) {
  return useQuery({
    queryKey: ['instanceGroups', 'subtree', groupId],
    queryFn: async () => {
      const { data } = await api.get<{ instanceIds: number[] }>(
        `/instance-groups/${groupId}/instances`,
      )
      return data.instanceIds
    },
    enabled: groupId != null && groupId > 0,
  })
}

/** 创建分组（parentId 省略=根分组）。 */
export function useCreateInstanceGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { name: string; parentId?: number | null }) =>
      api.post<InstanceGroupNode>('/instance-groups', body).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instanceGroups'] }),
  })
}

/**
 * 改名 / 移动父（防环）。
 * parentId 省略=不改父；null=移到根；数字=移到该父下。
 */
export function useUpdateInstanceGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({
      id,
      ...body
    }: {
      id: number
      name?: string
      parentId?: number | null
    }) => api.put<InstanceGroupNode>(`/instance-groups/${id}`, body).then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instanceGroups'] }),
  })
}

/** 删除分组（非空被后端拒删）。 */
export function useDeleteInstanceGroup() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/instance-groups/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instanceGroups'] }),
  })
}

/** 批量将实例加入分组（幂等）。 */
export function useAddInstanceGroupMembers() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, instanceIds }: { id: number; instanceIds: number[] }) =>
      api
        .post<{ added: number; members: InstanceGroupMember[] }>(
          `/instance-groups/${id}/members`,
          { instanceIds },
        )
        .then((r) => r.data),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instanceGroups'] }),
  })
}

/** 批量从分组移除实例（不影响实例本身）。 */
export function useRemoveInstanceGroupMembers() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, instanceIds }: { id: number; instanceIds: number[] }) =>
      api.delete(`/instance-groups/${id}/members`, { data: { instanceIds } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instanceGroups'] }),
  })
}
