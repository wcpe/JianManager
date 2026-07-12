import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 单条 registry 配置（FR-306）。scope 空=默认源；非空=@scope 域源。 */
export interface PMRegistry {
  name?: string
  url: string
  scope?: string
  /** 脱敏后回传掩码值（有凭据时），回传掩码=不改该源 token。 */
  token?: string
  tokenMasked?: boolean
}

/** 节点包管理器配置视图（FR-306）。 */
export interface PMConfigView {
  pm: 'npm' | 'pnpm' | 'yarn'
  corepackAvailable: boolean
  pmVersion: string
  nodeBin: string
  registries: PMRegistry[]
}

/** 读取节点 PM 配置（FR-306）。 */
export function useNodePMConfig(nodeId: number, opts?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['node-pm-config', nodeId],
    queryFn: async () => (await api.get<PMConfigView>(`/nodes/${nodeId}/pm-config`)).data,
    enabled: opts?.enabled ?? true,
  })
}

/** 保存节点 PM 配置（FR-306）：选 PM（corepack 激活 pnpm/yarn）+ registry。 */
export function useSetNodePMConfig(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: { pm: string; registries: PMRegistry[] }) =>
      (await api.put<PMConfigView>(`/nodes/${nodeId}/pm-config`, body)).data,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['node-pm-config', nodeId] })
    },
  })
}

/** 已装全局包（FR-307）。latest 非空=可更新到该版。 */
export interface GlobalPackage {
  name: string
  version: string
  latest?: string
}

/** 列出节点托管全局目录已装包（FR-307；查询较重，仅分区展开时启用）。 */
export function useGlobalPackages(nodeId: number, opts?: { enabled?: boolean }) {
  return useQuery({
    queryKey: ['node-global-packages', nodeId],
    queryFn: async () =>
      (await api.get<{ pm: string; packages: GlobalPackage[] }>(`/nodes/${nodeId}/packages`)).data,
    enabled: opts?.enabled ?? true,
    staleTime: 15_000,
  })
}

/** 异步安装/升级全局包（FR-307）：202+taskId，进度看任务中心；version 空=latest。 */
export function useInstallGlobalPackage(nodeId: number) {
  return useMutation({
    mutationFn: async (body: { name: string; version?: string }) =>
      (await api.post<{ taskId: string }>(`/nodes/${nodeId}/packages`, body)).data,
  })
}

/** 卸载全局包（FR-307，同步）。 */
export function useRemoveGlobalPackage(nodeId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (name: string) =>
      (await api.delete(`/nodes/${nodeId}/packages`, { params: { name } })).data,
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['node-global-packages', nodeId] }),
  })
}
