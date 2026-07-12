/**
 * 本地发布草稿的文件浏览器数据源适配器（FR-250）。
 *
 * FR-191/214 的预览走「已上传制品」（`clientDistSource` 经管理面 sha256 端点读文本）。FR-250 把发布
 * 改为**延迟批量上传**——预览时文件尚未上传、无 sha256，故预览须从**浏览器内 `File`** 直接读。
 * 本适配器把「path → 本地 File」列表适配成与后端解耦的 {@link FileBrowserSource}，复用共享
 * {@link FileBrowser}（FR-213）的浏览/预览/降级/高亮，零网络、零后端依赖。
 *
 * 二进制（含 NUL）/ 超大（1 MiB 阈值）判定沿用 {@link looksBinary}/{@link PREVIEW_MAX_BYTES}
 * （与 `instanceSource` 同口径）；下载经 object URL 触发（本地字节，不走服务端）。
 */
import type { FileBrowserSource, FileEntry, PreviewContent } from '../types'
import { PREVIEW_MAX_BYTES, looksBinary } from './instanceSource'

/** 本地草稿数据源的最小输入：目标相对路径 + 浏览器内 File（尚未上传）。 */
export interface LocalDraftFile {
  /** 相对 gameDir 的 POSIX 路径（= 文件树键）。 */
  path: string
  /** 浏览器内文件对象（内容源，按需 slice/读文本，不预载全量进内存）。 */
  file: File
}

/** 末段文件名（path 以 "/" 分隔；无段时回退原串）。 */
function baseName(path: string): string {
  const segs = path.split('/').filter((s) => s !== '')
  return segs.length > 0 ? segs[segs.length - 1] : path
}

/** 触发浏览器下载本地 File 并清理 object URL（与 @/api/clientVersions triggerDownload 同范式）。 */
function triggerLocalDownload(file: File, filename: string): void {
  const url = URL.createObjectURL(file)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

/**
 * 构建本地发布草稿的文件浏览器数据源（扁平全量，FR-250）。
 *
 * @param files 扁平本地草稿清单（path + 浏览器内 File）。
 *
 * `readContent`：超大按 `file.size` 直接降级（不读全量）；否则读文本 → 含 NUL 判二进制，
 * 反之作文本高亮。异常（读失败）交由 {@link FileBrowser} 捕获为 error 态。
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
