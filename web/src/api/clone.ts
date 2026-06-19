import { useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'
import type { InstanceInfo } from '@/api/instances'

/** 复制子服请求体（对应后端 service.CloneInstanceRequest，FR-036）。 */
export interface CloneBody {
  name: string
  motd?: string
  levelName?: string
  registerToProxyIds?: number[]
  dryRun?: boolean
}

/** 复制结果（dryRun 时 instance 为空）。 */
export interface CloneResult {
  instance?: InstanceInfo
  allocated: { workDir: string; serverPort: number; rconPort: number; queryPort: number }
  excluded: string[]
  registrations?: unknown[]
  warnings?: string[]
  dryRun: boolean
}

/** 复制源 backend 为独立新实例；dryRun=true 仅预检。 */
export function useCloneInstance(sourceId: number) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: CloneBody) =>
      api.post<CloneResult>(`/instances/${sourceId}/clone`, body).then((r) => r.data),
    onSuccess: (res) => {
      if (!res.dryRun) qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })
}
