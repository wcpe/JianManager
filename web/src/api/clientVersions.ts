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

/**
 * 管理面制品文本预览结果（FR-214，与后端 service.ArtifactTextPreview 对应）。
 * 降级由 kind 显式表达——前端据此渲染文本或降级为「仅下载」，不自行猜测。
 */
export interface ArtifactTextPreview {
  /** text=可文本预览 | binary=含 NUL/压缩仅下载 | too-large=超上限仅下载。 */
  kind: 'text' | 'binary' | 'too-large'
  /** UTF-8 文本（仅 kind=text）。 */
  content?: string
  /** 制品字节数（降级态展示）。 */
  size: number
  /** 制品压缩算法（none|zstd 等，信息性）。 */
  codec: string
}

/**
 * 读取客户端分发制品文本内容用于**管理台**预览（FR-214）。
 *
 * 走 JWT 平台管理员端点 `GET /client-channels/:id/files/content?sha256=`——玩家制品端点
 * `GET /client-artifacts/:sha256` 用拉取密钥、浏览器无之不能复用（ADR-022/023）。
 */
export async function fetchClientArtifactContent(
  channelId: string,
  sha256: string,
): Promise<ArtifactTextPreview> {
  const { data } = await api.get<ArtifactTextPreview>(
    `/client-channels/${channelId}/files/content`,
    { params: { sha256 } },
  )
  return data
}

/** 触发浏览器下载并清理 object URL（与 @/api/files 同范式）。 */
function triggerDownload(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

/**
 * 下载客户端分发制品（**管理台** JWT 端点，FR-214）。
 * downloadName 取 manifest path 末段以贴近原始文件名（制品按内容寻址，URL 用 sha256）。
 */
export async function downloadClientArtifact(
  channelId: string,
  sha256: string,
  downloadName?: string,
): Promise<void> {
  const { data } = await api.get(`/client-channels/${channelId}/files/download`, {
    params: { sha256 },
    responseType: 'blob',
  })
  triggerDownload(data as Blob, downloadName || sha256)
}

/** 发布/回滚后统一失效版本列表与频道（latest 指针随之变化）。 */
function invalidateChannelVersions(qc: ReturnType<typeof useQueryClient>, channelId: string) {
  qc.invalidateQueries({ queryKey: ['client-versions', channelId] })
  qc.invalidateQueries({ queryKey: ['client-channels'] })
  qc.invalidateQueries({ queryKey: ['client-channels', channelId] })
}
