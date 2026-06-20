import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 通用文件版本元数据（与后端 service.FileVersion 对应，FR-051）。 */
export interface FileVersion {
  id: number
  filePath: string
  size: number
  authorId: number
  createdAt: string
  rollbackOfVersionId?: number
}

/** 文件版本差异结果；二进制内容 binary=true 且 unifiedDiff 为空。 */
export interface FileVersionDiff {
  fromVersionId: number
  toVersionId: number
  unifiedDiff: string
  binary: boolean
}

/** 列出某文件的历史版本（按 ID 倒序，最新在前）。 */
export function useFileVersions(instanceId: number, filePath: string | null) {
  return useQuery({
    queryKey: ['fileVersions', instanceId, filePath],
    queryFn: async () => {
      const { data } = await api.get<FileVersion[]>(`/instances/${instanceId}/files/versions`, {
        params: { path: filePath },
      })
      return data
    },
    enabled: !!instanceId && !!filePath,
  })
}

/** 查询两版本之间的 diff（to 省略表示与当前文件比较）。 */
export function useFileVersionDiff(
  instanceId: number,
  filePath: string | null,
  fromId?: number,
  toId?: number,
) {
  return useQuery({
    queryKey: ['fileVersions', instanceId, 'diff', filePath, fromId, toId],
    queryFn: async () => {
      const { data } = await api.get<FileVersionDiff>(`/instances/${instanceId}/files/diff`, {
        params: { path: filePath, from: fromId, to: toId },
      })
      return data
    },
    enabled: !!instanceId && !!filePath && !!fromId && !!toId,
  })
}

/** 回滚到指定版本（回滚前后端自动快照当前内容）。 */
export function useRollbackFile(instanceId: number, filePath: string | null) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: { versionId: number }) => {
      const { data } = await api.post<{ versionId: number }>(`/instances/${instanceId}/files/rollback`, {
        path: filePath,
        versionId: payload.versionId,
      })
      return data
    },
    onSuccess: () => {
      // 回滚改变了文件内容并新增版本，失效版本列表缓存。
      qc.invalidateQueries({ queryKey: ['fileVersions', instanceId, filePath] })
    },
  })
}
