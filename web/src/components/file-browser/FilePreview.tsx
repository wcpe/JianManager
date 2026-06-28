import { useTranslation } from 'react-i18next'
import { Download, FileQuestion, FileWarning, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import CodeEditor from '@/components/explorer/editor/CodeEditor'
import type { FileEntry, PreviewContent } from './types'

/** 字节数转人类可读。 */
function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

interface FilePreviewProps {
  /** 当前选中的文件（null=未选中，显示引导占位）。 */
  entry: FileEntry | null
  /** 预览内容（null + entry 非空 = 加载中）。 */
  content: PreviewContent | null
  /** 是否正在读取。 */
  loading: boolean
  /** 下载当前文件（省略则降级态不显示下载按钮）。 */
  onDownload?: (entry: FileEntry) => void
}

/**
 * 共享文件浏览器的只读内容预览（FR-213）。
 *
 * 复用 FR-070 `CodeEditor`（只读 + 按文件名多格式高亮：yaml/json/properties/toml/...）渲染文本；
 * 二进制 / 超大 / 错误三类**降级为占位**（不渲染编辑器），且降级态保留下载入口——
 * 「不可预览必可下载」。降级由 {@link PreviewContent.kind} 显式驱动，本组件不自行判定。
 */
export default function FilePreview({ entry, content, loading, onDownload }: FilePreviewProps) {
  const { t } = useTranslation()

  // 未选中文件：引导占位。
  if (!entry) {
    return (
      <div className="flex flex-1 items-center justify-center p-6 text-center text-sm text-muted-foreground">
        {t('fileBrowser.pickFile')}
      </div>
    )
  }

  // 加载中。
  if (loading || content === null) {
    return (
      <div className="flex flex-1 items-center justify-center text-sm text-muted-foreground">
        <Loader2 className="mr-2 size-4 animate-spin" /> {t('fileBrowser.loading')}
      </div>
    )
  }

  // 文本：只读编辑器高亮预览。
  if (content.kind === 'text') {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex items-center justify-between border-b bg-muted/20 px-2 py-1 text-xs">
          <span className="truncate font-medium">{entry.name}</span>
          <span className="ml-2 flex shrink-0 items-center gap-2 text-muted-foreground">
            {content.truncated && <span>{t('fileBrowser.truncated')}</span>}
            {onDownload && (
              <button
                type="button"
                className="inline-flex items-center gap-1 hover:text-foreground"
                onClick={() => onDownload(entry)}
              >
                <Download className="size-3.5" /> {t('fileBrowser.download')}
              </button>
            )}
          </span>
        </div>
        <div className="min-h-0 flex-1">
          <CodeEditor value={content.content} filename={entry.name} readOnly />
        </div>
      </div>
    )
  }

  // 降级态（二进制 / 超大 / 错误）：占位 + 下载兜底。
  const isError = content.kind === 'error'
  const message =
    content.kind === 'binary'
      ? t('fileBrowser.binaryNotice')
      : content.kind === 'too-large'
        ? t('fileBrowser.tooLargeNotice', { size: formatBytes(content.size) })
        : content.message

  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center">
      {isError ? (
        <FileWarning className="size-8 text-destructive" />
      ) : (
        <FileQuestion className="size-8 text-muted-foreground" />
      )}
      <p className={`max-w-sm text-sm ${isError ? 'text-destructive' : 'text-muted-foreground'}`}>
        {message}
      </p>
      <p className="font-mono text-xs text-muted-foreground">{entry.name}</p>
      {!isError && onDownload && (
        <Button size="sm" variant="outline" className="gap-1" onClick={() => onDownload(entry)}>
          <Download className="size-3.5" /> {t('fileBrowser.download')}
        </Button>
      )}
    </div>
  )
}
