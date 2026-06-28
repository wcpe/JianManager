/**
 * 客户端分发的文件浏览器数据源适配器（FR-214）。
 *
 * 把「版本文件清单 / 发布草稿」这类**扁平 manifest 文件列表**适配成与后端解耦的 {@link FileBrowserSource}，
 * 使共享 {@link FileBrowser} 能用于客户端分发的**只读浏览 / 内容预览 / 下载**——复用 FR-213 的预览/降级/高亮，
 * 不再为客户端分发另写一套（仿 instanceSource 范式）。
 *
 * 与实例工作目录（懒加载分层）不同，客户端分发的文件清单是**扁平全量**的（manifest 一次给全），
 * 故 `flat=true`，组件内部 {@link buildTree} 一次成树。
 *
 * 内容预览经**管理面** JWT 端点 `GET /client-channels/:id/files/content`（按制品 sha256 读文本）——
 * 玩家制品端点 `GET /client-artifacts/:sha256` 走拉取密钥、浏览器无之不能复用（ADR-022/023）。
 * 二进制 / 压缩 / 超大降级由后端按 kind 显式返回，组件只消费结果。
 */
import {
  fetchClientArtifactContent,
  downloadClientArtifact,
  type ManifestFile,
} from '@/api/clientVersions'
import type { FileBrowserSource, FileEntry, PreviewContent } from '../types'

/**
 * 客户端分发数据源的最小文件输入（与 {@link ManifestFile} / 发布草稿兼容）。
 * 各调用方把自身形态（version 详情 `ManifestFile` / 发布草稿 `DraftFile`）映射到此最小集合。
 */
export interface ClientDistFile {
  /** 相对 gameDir 的 POSIX 路径（= 文件树键）。 */
  path: string
  /** 解压后原始内容字节数（展示用）。 */
  size: number
  /** 下载制品 sha256（= manifest `files[].artifact.sha256`），内容寻址下载/预览的 key。 */
  artifactSha: string
}

/** 把版本详情的 {@link ManifestFile} 映射为数据源输入（取 artifact.sha256 作内容寻址 key）。 */
export function manifestFilesToDistFiles(files: ManifestFile[]): ClientDistFile[] {
  return files.map((f) => ({ path: f.path, size: f.size, artifactSha: f.artifact?.sha256 ?? '' }))
}

/** 末段文件名（path 以 "/" 分隔；无段时回退原串）。 */
function baseName(path: string): string {
  const segs = path.split('/').filter((s) => s !== '')
  return segs.length > 0 ? segs[segs.length - 1] : path
}

/**
 * 构建客户端分发文件浏览器数据源（扁平全量，FR-214）。
 *
 * @param channelId 频道 ID（内容/下载端点按频道路由，端点本身按 sha256 内容寻址）。
 * @param files     扁平文件清单（版本详情或发布草稿，经各自映射为 {@link ClientDistFile}）。
 *
 * 缺制品 sha（如 `sync=ignore` 的占位文件无 artifact）→ readContent 返回错误占位、download 不触发。
 */
export function clientDistSource(channelId: string, files: ClientDistFile[]): FileBrowserSource {
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
        return { kind: 'error', message: '该文件无可预览的制品内容' }
      }
      const res = await fetchClientArtifactContent(channelId, f.artifactSha)
      if (res.kind === 'text') return { kind: 'text', content: res.content ?? '' }
      if (res.kind === 'too-large') return { kind: 'too-large', size: res.size }
      return { kind: 'binary' }
    },
    download: (entry: FileEntry) => {
      const f = byPath.get(entry.path)
      if (!f || !f.artifactSha) return
      return downloadClientArtifact(channelId, f.artifactSha, baseName(f.path))
    },
  }
}
