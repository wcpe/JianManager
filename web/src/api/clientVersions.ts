import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** manifest 文件的下载制品引用（contract §2 files[].artifact）。 */
export interface ManifestArtifact {
  /** 制品自身 sha256 = 下载寻址 key。 */
  sha256: string
  size: number
  /** 压缩算法：zstd | none。 */
  codec: string
}

/**
 * manifest 单文件条目（contract §2 files[]）。
 * sha256/md5/size 描述**解压后原始内容**；artifact 描述下载制品（压缩态）。
 */
export interface ManifestFile {
  /** 相对 gameDir 的 POSIX 路径。 */
  path: string
  sha256: string
  md5: string
  size: number
  /** 同步策略：strict=强制一致 | once=仅缺失时写 | ignore=不动。 */
  sync: 'strict' | 'once' | 'ignore'
  /** 平台门控：空=全平台 | windows | macos | linux。 */
  platform: '' | 'windows' | 'macos' | 'linux'
  artifact: ManifestArtifact
}

/** 楔子 + updater-core 自更新段（contract §2 agent）。FR-088 向导透传原值、不编辑。 */
export interface ManifestAgent {
  wedge?: { version: number }
  core?: { version: number; platforms: Record<string, ManifestArtifact> }
}

/** 版本历史列表项（仅管理面，不向玩家暴露）。 */
export interface ClientVersionSummary {
  version: number
  note: string
  fileCount: number
  createdBy: number
  createdAt: string
  /** 是否为频道当前 latest 指针所指版本。 */
  isLatest: boolean
}

/** 版本详情（含完整文件清单）。 */
export interface ClientVersionDetail {
  version: number
  note: string
  createdBy: number
  createdAt: string
  isLatest: boolean
  managedDirs: string[]
  files: ManifestFile[]
  agent?: ManifestAgent
}

/** 上传制品结果。codec=none 时 sha256/md5/size 即解压后原始内容元数据（供向导自动填充）。 */
export interface ClientFileResult {
  sha256: string
  md5: string
  size: number
  codec: string
}

/** 版本历史列表（版本号 DESC）。 */
export function useClientVersions(channelId: string | null) {
  return useQuery({
    queryKey: ['client-versions', channelId],
    queryFn: async () => {
      const { data } = await api.get<ClientVersionSummary[]>(`/client-channels/${channelId}/versions`)
      return data
    },
    enabled: !!channelId,
  })
}

/** 版本详情（文件清单）。 */
export function useClientVersion(channelId: string | null, version: number | null) {
  return useQuery({
    queryKey: ['client-versions', channelId, version],
    queryFn: async () => {
      const { data } = await api.get<ClientVersionDetail>(`/client-channels/${channelId}/versions/${version}`)
      return data
    },
    enabled: !!channelId && version != null,
  })
}

/** 上传单个客户端文件制品（multipart），返回内容寻址元数据。 */
export function usePublishClientFile() {
  return useMutation({
    mutationFn: async ({ channelId, file, codec }: { channelId: string; file: File; codec?: string }) => {
      const form = new FormData()
      form.append('file', file)
      form.append('codec', codec ?? 'none')
      const { data } = await api.post<ClientFileResult>(`/client-channels/${channelId}/files`, form, {
        headers: { 'Content-Type': 'multipart/form-data' },
      })
      return data
    },
  })
}

/** 发布版本（提交文件清单 + 托管目录 + 自更新段）；服务端单调递增版本号、切 latest 指针。 */
export function usePublishClientVersion() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (vars: {
      channelId: string
      files: ManifestFile[]
      managedDirs: string[]
      agent?: ManifestAgent | null
      note?: string
    }) => {
      const { data } = await api.post(`/client-channels/${vars.channelId}/versions`, {
        files: vars.files,
        managedDirs: vars.managedDirs,
        agent: vars.agent ?? undefined,
        note: vars.note,
      })
      return data
    },
    onSuccess: (_d, vars) => invalidateChannelVersions(qc, vars.channelId),
  })
}

/** 运营回滚：以更高版本号重发历史版本内容为新 latest（保持单调、不触发客户端防降级）。 */
export function useRollbackClientVersion() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (vars: { channelId: string; sourceVersion: number; note?: string }) => {
      const { data } = await api.post(`/client-channels/${vars.channelId}/rollback`, {
        sourceVersion: vars.sourceVersion,
        note: vars.note,
      })
      return data
    },
    onSuccess: (_d, vars) => invalidateChannelVersions(qc, vars.channelId),
  })
}

/** 发布/回滚后统一失效版本列表与频道（latest 指针随之变化）。 */
function invalidateChannelVersions(qc: ReturnType<typeof useQueryClient>, channelId: string) {
  qc.invalidateQueries({ queryKey: ['client-versions', channelId] })
  qc.invalidateQueries({ queryKey: ['client-channels'] })
  qc.invalidateQueries({ queryKey: ['client-channels', channelId] })
}
