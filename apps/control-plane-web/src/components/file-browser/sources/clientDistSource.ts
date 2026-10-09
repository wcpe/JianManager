/**
 * 客户端分发的文件浏览器数据源适配器（FR-214）。
 *
 * 把「版本文件清单 / 发布草稿」这类**扁平 manifest 文件列表**适配成与后端解耦的
 * `FileBrowserSource`，使共享 `FileBrowser` 能用于客户端分发的**只读浏览 / 内容预览 / 下载**。
 *
 * 视图与工厂已回迁应用侧（原 ADR-097 迁包已撤销），本层只注入端点实现（纯接线层）：
 * 内容预览经**管理面** JWT 端点按制品 sha256 读文本（玩家制品端点走拉取密钥，浏览器无之不能复用）。
 */
import { fetchClientArtifactContent, downloadClientArtifact } from '@/api/clientVersions'
import {
  createClientDistSource,
  manifestFilesToDistFiles,
  type ClientDistApi,
  type ClientDistFile,
  type ClientDistSourceMessages,
} from '@/lib/file-sources'

export { manifestFilesToDistFiles }
export type { ClientDistFile, ClientDistSourceMessages }

/** 客户端分发端点实现（与包内 `ClientDistApi` 对齐）。 */
const clientDistApi: ClientDistApi = {
  fetchContent: (channelId, sha) => fetchClientArtifactContent(channelId, sha),
  download: (channelId, sha, filename) => {
    downloadClientArtifact(channelId, sha, filename)
  },
}

/**
 * 构建客户端分发文件浏览器数据源（扁平全量，FR-214）。
 *
 * 缺制品 sha（如 `sync=ignore` 的占位文件无 artifact）→ readContent 返回错误占位、download 不触发。
 */
export function clientDistSource(
  channelId: string,
  files: ClientDistFile[],
  messages: ClientDistSourceMessages = {},
) {
  return createClientDistSource(channelId, files, clientDistApi, messages)
}
