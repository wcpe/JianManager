import { useCallback, useEffect, useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import FileBrowserTree from './FileBrowserTree'
import FilePreview from './FilePreview'
import type { FileBrowserAction, FileBrowserSource, FileEntry, PreviewContent } from './types'

export type { FileBrowserAction, FileBrowserSource, FileEntry, PreviewContent } from './types'

interface FileBrowserProps {
  /** 数据源（注入；与具体后端解耦）。 */
  source: FileBrowserSource
  /**
   * 只读浏览（默认 true）。仅影响是否渲染注入的 `actions`——
   * 即便 false，组件本身也不含写端点，所有操作经 `actions` 注入。
   */
  readOnly?: boolean
  /** 可操作态的额外行操作（下载/重命名/删除等，全部由调用方实现）。 */
  actions?: FileBrowserAction[]
  /** 选中文件变化回调（供外部联动）。 */
  onSelect?: (entry: FileEntry | null) => void
  /** 数据刷新信号：值变化时重新拉取树（外部增删改后递增）。 */
  refreshKey?: number
  /** 容器附加类（默认占满父容器高度）。 */
  className?: string
}

/**
 * 共享文件浏览器（FR-213）。
 *
 * 双栏：左目录树/列表 + 右内容预览（文本多格式高亮 / 二进制·超大·错误降级 + 下载兜底）。
 * **展示型、数据源经 props 注入、不耦合具体后端**——主体只依赖 {@link FileBrowserSource} 契约，
 * 不 import 任何后端 api。实例工作目录与客户端分发 manifest（FR-214）各提供一份适配器即可复用。
 *
 * `readOnly`（默认）为纯浏览；注入 `actions` 后文件行出现行操作菜单。
 * 选中文件→经 `source.readContent` 读出 {@link PreviewContent}，由 {@link FilePreview} 按 kind 渲染/降级。
 */
export default function FileBrowser({
  source,
  readOnly = true,
  actions = [],
  onSelect,
  refreshKey = 0,
  className,
}: FileBrowserProps) {
  const [selected, setSelected] = useState<FileEntry | null>(null)
  const [content, setContent] = useState<PreviewContent | null>(null)
  const [loading, setLoading] = useState(false)
  // 用递增 token 丢弃过期的读取响应（快速切文件时防错位）。
  const reqToken = useRef(0)

  const effectiveActions = readOnly ? [] : actions

  const selectFile = useCallback(
    (entry: FileEntry) => {
      setSelected(entry)
      onSelect?.(entry)
      const token = ++reqToken.current
      setLoading(true)
      setContent(null)
      void source
        .readContent(entry)
        .then((res) => {
          if (token === reqToken.current) setContent(res)
        })
        .catch((e: unknown) => {
          if (token === reqToken.current) {
            setContent({ kind: 'error', message: e instanceof Error ? e.message : String(e) })
          }
        })
        .finally(() => {
          if (token === reqToken.current) setLoading(false)
        })
    },
    [source, onSelect],
  )

  // 数据源切换或刷新时清空选中预览（避免展示旧数据源/已删条目）。
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- 源/刷新变化时复位预览，属合法同步
    setSelected(null)
    setContent(null)
    onSelect?.(null)
    reqToken.current++
    // onSelect 故意不入依赖：仅在源/刷新变化时复位一次。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source, refreshKey])

  return (
    <div className={cn('flex h-[600px] overflow-hidden rounded-lg border', className)}>
      {/* 左：目录树/列表 */}
      <div className="w-64 shrink-0 overflow-auto border-r bg-muted/20 p-1">
        <FileBrowserTree
          source={source}
          selectedPath={selected?.path ?? null}
          onSelectFile={selectFile}
          actions={effectiveActions}
          refreshKey={refreshKey}
        />
      </div>
      {/* 右：内容预览 */}
      <div className="flex min-w-0 flex-1 flex-col">
        <FilePreview entry={selected} content={content} loading={loading} onDownload={source.download} />
      </div>
    </div>
  )
}
