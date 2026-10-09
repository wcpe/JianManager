/**
 * 实例工作目录的文件浏览器数据源适配器（FR-213）。
 *
 * 把既有实例文件端点（`@/api/files`，FR-008/070）适配成与后端解耦的 `FileBrowserSource`，
 * 使共享 `FileBrowser` 能用于实例工作目录的**只读浏览 / 内容预览 / 下载**。
 * 二进制 / 超大判定的口径在此（适配器决定，组件只消费结果）。
 *
 * 视图与工厂已回迁应用侧（原 ADR-097 迁包已撤销），本层只注入端点实现（纯接线层）。
 */
import { fetchFileList, readFileContent, downloadFile } from '@/api/files'
import { createInstanceFileSource, looksBinary, PREVIEW_MAX_BYTES } from '@/lib/file-sources'
import type { InstanceFileApi } from '@/lib/file-sources'

export { looksBinary, PREVIEW_MAX_BYTES }

/** 实例文件端点实现（与包内 `InstanceFileApi` 对齐）。 */
const instanceFileApi: InstanceFileApi = { fetchFileList, readFileContent, downloadFile }

/**
 * 构建实例工作目录数据源。
 * 懒加载分层：点目录展开时拉该层（`GET /instances/:id/files?path=`）。
 */
export function instanceFileSource(instanceId: number) {
  return createInstanceFileSource(instanceId, instanceFileApi)
}
