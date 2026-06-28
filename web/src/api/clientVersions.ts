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

// ===== updater-core 集中版本管理（FR-193，见 ADR-045）=====
// 楔子冻结、不纳管；仅 updater-core 经 manifest agent.core 做 pin/更新/回退的集中版本管理。
// 两条版本轴：manifest version（内容、防降级）与 agent.core.version（core 自身、由 pin 驱动、对客户端只升不降）。

/** updater-core 版本列表项（管理面）。 */
export interface ClientCoreVersionSummary {
  /** core 自身版本号 = manifest agent.core.version。 */
  version: number
  /** 制品自身 sha256（三平台同此值，一份 jar 通用）。 */
  artifactSha256: string
  artifactSize: number
  codec: string
  note: string
  /** 回退来源版本号（>0=该版本是某历史版本的重发，0=直接上传）。 */
  sourceVersion: number
  createdBy: number
  createdAt: string
}

/** core 版本列表响应（含冻结楔子版本，只读展示）。 */
export interface ClientCoreVersionsResponse {
  versions: ClientCoreVersionSummary[]
  /** 楔子冻结、单版本、不纳入版本管理（仅展示版本号）。 */
  wedge: { version: string; frozen: boolean }
}

/** 频道 core pin 现状。 */
export interface ClientCorePin {
  channelId: string
  /** 频道 pin 的 core 版本号（0=自动用最新已登记）。 */
  pinnedCoreVersion: number
  /** 解析后的有效版本（无任何已登记 core 时为 0）。 */
  effectiveVersion: number
}

/** core 版本列表（版本号 DESC）+ 冻结楔子版本。 */
export function useClientCoreVersions(channelId: string | null) {
  return useQuery({
    queryKey: ['client-core-versions', channelId],
    queryFn: async () => {
      const { data } = await api.get<ClientCoreVersionsResponse>('/client-core-versions')
      return data
    },
    enabled: !!channelId,
  })
}

/** 频道 core pin 现状（pinnedCoreVersion + effectiveVersion）。 */
export function useClientCorePin(channelId: string | null) {
  return useQuery({
    queryKey: ['client-core-pin', channelId],
    queryFn: async () => {
      const { data } = await api.get<ClientCorePin>(`/client-channels/${channelId}/core-pin`)
      return data
    },
    enabled: !!channelId,
  })
}

/** 上传一份 updater-core jar 制品（multipart），返回内容寻址元数据。 */
export function useUploadClientCore() {
  return useMutation({
    mutationFn: async ({ file, codec }: { file: File; codec?: string }) => {
      const form = new FormData()
      form.append('file', file)
      form.append('codec', codec ?? 'none')
      const { data } = await api.post<ClientFileResult>('/client-core-versions/upload', form, {
        headers: { 'Content-Type': 'multipart/form-data' },
      })
      return data
    },
  })
}

/** 登记 core 版本（服务端单调递增分配版本号）。上传后调用，传上传返回的内容寻址元数据。 */
export function useRegisterClientCoreVersion() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (vars: {
      channelId: string
      artifactSha256: string
      artifactSize: number
      codec?: string
      note?: string
    }) => {
      const { data } = await api.post('/client-core-versions', {
        artifactSha256: vars.artifactSha256,
        artifactSize: vars.artifactSize,
        codec: vars.codec ?? 'none',
        note: vars.note,
      })
      return data
    },
    onSuccess: (_d, vars) => invalidateCoreVersions(qc, vars.channelId),
  })
}

/** 设/更新频道 core pin（version=0 恢复自动用最新；>0 须为已登记版本）。 */
export function useSetClientCorePin() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (vars: { channelId: string; version: number }) => {
      const { data } = await api.put(`/client-channels/${vars.channelId}/core-pin`, { version: vars.version })
      return data
    },
    onSuccess: (_d, vars) => invalidateCoreVersions(qc, vars.channelId),
  })
}

/** 回退坏 core：以更高版本号重发历史 core 字节为新版并 pin（不降 agent.core.version）。 */
export function useRollbackClientCore() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (vars: { channelId: string; sourceVersion: number; note?: string }) => {
      const { data } = await api.post(`/client-channels/${vars.channelId}/core-rollback`, {
        sourceVersion: vars.sourceVersion,
        note: vars.note,
      })
      return data
    },
    onSuccess: (_d, vars) => invalidateCoreVersions(qc, vars.channelId),
  })
}

/** core 版本变更后失效 core 版本列表 + 频道 pin 现状。 */
function invalidateCoreVersions(qc: ReturnType<typeof useQueryClient>, channelId: string) {
  qc.invalidateQueries({ queryKey: ['client-core-versions'] })
  qc.invalidateQueries({ queryKey: ['client-core-pin', channelId] })
}
