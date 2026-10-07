/**
 * 文件浏览器数据源工厂（FR-213 / FR-214 / FR-250）。
 *
 * 把「实例工作目录 / 本地发布草稿 / 客户端分发制品」三种形态适配成与后端解耦的
 * {@link FileBrowserSource}，供共享 `FileBrowser` 复用其浏览 / 预览 / 降级 / 高亮能力。
 *
 * 受控（ADR-097）：**不直接调用应用侧 api**——涉及网络的两种（实例、客户端分发）
 * 由调用方注入端点实现；本地草稿零网络，直接可用。
 */
import type { FileBrowserSource, FileEntry, PreviewContent } from './file-browser-types'
import { joinPath } from './paths'

/** 超过此字节数的文件不读全量、降级为「仅下载」。 */
export const PREVIEW_MAX_BYTES = 1024 * 1024 // 1 MiB

/** 含 NUL 字节即判为二进制（与 ArchiveViewer 二进制判定范式一致的启发式）。 */
export function looksBinary(text: string): boolean {
  return /\0/.test(text)
}

/** 末段文件名（path 以 "/" 分隔；无段时回退原串）。 */
function baseName(path: string): string {
  const segs = path.split('/').filter((s) => s !== '')
  return segs.length > 0 ? segs[segs.length - 1] : path
}

// ── 实例工作目录（FR-213）：懒加载分层 ────────────────────────────────

/** 实例文件端点最小接口（应用侧注入 `@/api/files` 实现）。 */
export interface InstanceFileApi {
  fetchFileList: (
    instanceId: number,
    dirPath: string,
  ) => Promise<{ name: string; isDir: boolean; size: number; modTime: number }[]>
  readFileContent: (instanceId: number, path: string) => Promise<string>
  downloadFile: (instanceId: number, path: string) => void
}

/**
 * 构建实例工作目录数据源（只读浏览 / 预览 / 下载）。
 * 懒加载分层：点目录展开时拉该层。
 */
export function createInstanceFileSource(instanceId: number, api: InstanceFileApi): FileBrowserSource {
  return {
    flat: false,
    list: async (dirPath: string): Promise<FileEntry[]> => {
      const data = await api.fetchFileList(instanceId, dirPath)
      return data.map((f) => ({
        path: joinPath(dirPath, f.name),
        name: f.name,
        isDir: f.isDir,
        size: f.size,
        modTime: f.modTime,
      }))
    },
    readContent: async (entry: FileEntry): Promise<PreviewContent> => {
      // 超大：不读全量，直接降级（按列表给出的 size 判定）。
      if (entry.size != null && entry.size > PREVIEW_MAX_BYTES) {
        return { kind: 'too-large', size: entry.size }
      }
      const text = await api.readFileContent(instanceId, entry.path)
      if (looksBinary(text)) return { kind: 'binary' }
      return { kind: 'text', content: text }
    },
    download: (entry: FileEntry) => api.downloadFile(instanceId, entry.path),
  }
}

// ── 本地发布草稿（FR-250）：零网络 ───────────────────────────────────

/** 本地草稿数据源的最小输入：目标相对路径 + 浏览器内 File（尚未上传）。 */
export interface LocalDraftFile {
  /** 相对 gameDir 的 POSIX 路径（= 文件树键）。 */
  path: string
  /** 浏览器内文件对象（内容源，按需 slice/读文本，不预载全量进内存）。 */
  file: File
}

/** 触发浏览器下载本地 File 并清理 object URL。 */
function triggerLocalDownload(file: File, filename: string): void {
  const url = URL.createObjectURL(file)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

/**
 * 构建本地发布草稿的文件浏览器数据源（扁平全量）。
 *
 * 预览从浏览器内 `File` 直接读（此时文件尚未上传、无 sha256）；二进制 / 超大判定
 * 与实例源同口径；下载经 object URL 触发（本地字节，不走服务端）。
 */
export function localDraftSource(files: LocalDraftFile[]): FileBrowserSource {
  // path → File，供 readContent/download 据条目 path 反查本地文件（FileEntry 不带 File）。
  const byPath = new Map<string, File>()
  for (const f of files) byPath.set(f.path, f.file)

  return {
    flat: true,
    list: async (): Promise<FileEntry[]> =>
      files.map((f) => ({ path: f.path, name: baseName(f.path), isDir: false, size: f.file.size })),
    readContent: async (entry: FileEntry): Promise<PreviewContent> => {
      const file = byPath.get(entry.path)
      if (!file) return { kind: 'error', message: '该文件不在本地草稿中' }
      // 超大：不读全量，直接降级。
      if (file.size > PREVIEW_MAX_BYTES) return { kind: 'too-large', size: file.size }
      const text = await file.text()
      if (looksBinary(text)) return { kind: 'binary' }
      return { kind: 'text', content: text }
    },
    download: (entry: FileEntry) => {
      const file = byPath.get(entry.path)
      if (!file) return
      triggerLocalDownload(file, baseName(entry.path))
    },
  }
}

// ── 客户端分发（FR-214）：扁平全量 + 管理面端点 ───────────────────────

/** 客户端分发数据源的最小文件输入（版本详情 / 发布草稿各自映射到此）。 */
export interface ClientDistFile {
  /** 相对 gameDir 的 POSIX 路径（= 文件树键）。 */
  path: string
  /** 解压后原始内容字节数（展示用）。 */
  size: number
  /** 下载制品 sha256（内容寻址下载/预览的 key）。 */
  artifactSha: string
}

/** 把版本详情的 manifest 文件映射为数据源输入（取 artifact.sha256 作内容寻址 key）。 */
export function manifestFilesToDistFiles(
  files: { path: string; size: number; artifact?: { sha256?: string } }[],
): ClientDistFile[] {
  return files.map((f) => ({ path: f.path, size: f.size, artifactSha: f.artifact?.sha256 ?? '' }))
}

/** 客户端分发端点最小接口（应用侧注入 `@/api/clientVersions` 实现）。 */
export interface ClientDistApi {
  /** 按制品 sha256 读文本（管理面 JWT 端点）。 */
  fetchContent: (
    channelId: string,
    sha: string,
  ) => Promise<{ kind: string; content?: string; size?: number }>
  /** 按制品 sha256 下载。 */
  download: (channelId: string, sha: string, filename: string) => void
}

/** 数据源文案（调用方按 i18n 传入；缺省用内置中文兜底）。 */
export interface ClientDistSourceMessages {
  noArtifactContent?: string
  artifactMissing?: string
  artifactPreviewFailed?: string
}

/**
 * 构建客户端分发文件浏览器数据源（扁平全量）。
 *
 * 缺制品 sha（如 `sync=ignore` 的占位文件无 artifact）→ readContent 返回错误占位、download 不触发。
 */
export function createClientDistSource(
  channelId: string,
  files: ClientDistFile[],
  api: ClientDistApi,
  messages: ClientDistSourceMessages = {},
): FileBrowserSource {
  // path → 文件，供 readContent/download 据条目 path 反查制品 sha（FileEntry 不带 sha）。
  const byPath = new Map<string, ClientDistFile>()
  for (const f of files) byPath.set(f.path, f)

  return {
    flat: true,
    list: async (): Promise<FileEntry[]> =>
      files.map((f) => ({ path: f.path, name: baseName(f.path), isDir: false, size: f.size })),
    readContent: async (entry: FileEntry): Promise<PreviewContent> => {
      const f = byPath.get(entry.path)
      if (!f || !f.artifactSha) {
        return { kind: 'error', message: messages.noArtifactContent ?? '该文件无可预览的制品内容' }
      }
      let res
      try {
        res = await api.fetchContent(channelId, f.artifactSha)
      } catch (err) {
        const apiErr = err as Error & { response?: { status?: number; data?: { error?: string } } }
        if (apiErr.response?.status === 404 || apiErr.response?.data?.error === 'ARTIFACT_NOT_FOUND') {
          return { kind: 'error', message: messages.artifactMissing ?? '制品内容不存在，可能已从制品库删除。' }
        }
        return { kind: 'error', message: messages.artifactPreviewFailed ?? '读取制品内容失败。' }
      }
      if (res.kind === 'text') return { kind: 'text', content: res.content ?? '' }
      if (res.kind === 'too-large') return { kind: 'too-large', size: res.size ?? 0 }
      return { kind: 'binary' }
    },
    download: (entry: FileEntry) => {
      const f = byPath.get(entry.path)
      if (!f || !f.artifactSha) return
      api.download(channelId, f.artifactSha, baseName(f.path))
    },
  }
}
