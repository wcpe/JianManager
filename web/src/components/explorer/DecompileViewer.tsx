import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Loader2, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { decompile } from '@/api/archive'
import CodeEditor from './editor/CodeEditor'

/**
 * 单文件反编译视图（FR-075）：对工作目录内某 .class/.jar 调 Worker CFR 反编译，
 * 结果只读展示（Java 高亮）。失败/降级显示降级提示，超时/截断有态。
 */
interface DecompileViewerProps {
  instanceId: number
  /** 目标相对工作目录路径（.class 或 .jar）。 */
  path: string
  /** 文件名（标题）。 */
  name: string
  onClose: () => void
}

export default function DecompileViewer({ instanceId, path, name, onClose }: DecompileViewerProps) {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(true)
  const [source, setSource] = useState('')
  const [decompiler, setDecompiler] = useState('')
  const [truncated, setTruncated] = useState(false)
  const [failed, setFailed] = useState('')

  useEffect(() => {
    let alive = true
    // 切换目标（instanceId/path 变化）时复位反编译态再异步请求，属合法同步。
    /* eslint-disable react-hooks/set-state-in-effect */
    setLoading(true)
    setFailed('')
    setSource('')
    /* eslint-enable react-hooks/set-state-in-effect */
    decompile(instanceId, path)
      .then((res) => {
        if (!alive) return
        if (!res.success) {
          setFailed(res.error || t('archive.decompileFailed'))
          return
        }
        setSource(res.source)
        setDecompiler(res.decompiler ?? '')
        setTruncated(res.truncated)
      })
      .catch(() => {
        if (alive) setFailed(t('archive.decompileFailed'))
      })
      .finally(() => {
        if (alive) setLoading(false)
      })
    return () => {
      alive = false
    }
  }, [instanceId, path, t])

  return (
    <div className="flex w-1/2 min-w-0 flex-col">
      <div className="flex items-center justify-between border-b bg-muted/30 px-2 py-1 text-sm">
        <span className="truncate font-medium">
          {t('archive.decompile')}: {name}
        </span>
        <span className="ml-2 flex shrink-0 items-center gap-2 text-xs text-muted-foreground">
          {decompiler}
          {truncated && ` · ${t('archive.truncated')}`}
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-1.5"
            title={t('common.close')}
            onClick={onClose}
          >
            <X className="size-3.5" />
          </Button>
        </span>
      </div>
      <div className="min-h-0 flex-1">
        {loading ? (
          <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
            <Loader2 className="mr-2 size-4 animate-spin" /> {t('archive.processing')}
          </div>
        ) : failed ? (
          <div className="p-3 text-sm text-destructive">{failed}</div>
        ) : (
          <CodeEditor value={source} filename={name + '.java'} readOnly />
        )}
      </div>
    </div>
  )
}
