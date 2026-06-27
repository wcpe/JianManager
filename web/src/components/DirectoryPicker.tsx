import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Folder, FolderUp, RefreshCw, Check } from 'lucide-react'
import { useBrowseDir } from '@/api/nodeRuntime'
import { Button } from '@/components/ui/button'

/** 节点目录选择器（FR-178）：逐级浏览节点上的目录、选定一个绝对路径用于 JDK 登记。 */
interface DirectoryPickerProps {
  /** 节点 ID（经 CP 委托 Worker 浏览）。 */
  nodeId: number
  /** 选定目录回调（传回绝对路径）。 */
  onPick: (path: string) => void
  /** 取消/关闭回调。 */
  onCancel: () => void
  /** 初始浏览路径（空=起点：盘符/根）。 */
  initialPath?: string
}

/**
 * 目录选择器：内联在登记表单内的稳定子视图（不切换隐显致布局重组，符合抽屉 UX 约束）。
 * 顶部显示当前路径与「选定此目录」，列表逐级进入子目录、可回到上级。
 */
export default function DirectoryPicker({ nodeId, onPick, onCancel, initialPath = '' }: DirectoryPickerProps) {
  const { t } = useTranslation()
  const [path, setPath] = useState(initialPath)
  const { data, isLoading, isError, error, refetch, isFetching } = useBrowseDir(nodeId, path)

  const current = data?.path ?? path
  const errMsg = (error as { response?: { data?: { message?: string } } })?.response?.data?.message

  return (
    <div className="rounded-md border bg-muted/30 p-3 space-y-2">
      <div className="flex items-center gap-2">
        <span className="text-xs text-muted-foreground shrink-0">{t('artifactCache.browseCurrent')}</span>
        <code className="flex-1 truncate rounded bg-background px-2 py-1 text-xs font-mono" title={current || '/'}>
          {current || t('artifactCache.browseRoots')}
        </code>
        <button
          type="button"
          onClick={() => refetch()}
          className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
          title={t('common.refresh')}
        >
          <RefreshCw className={`size-3.5 ${isFetching ? 'animate-spin' : ''}`} />
        </button>
      </div>

      <div className="max-h-56 overflow-y-auto rounded border bg-background">
        {data?.parent !== undefined && data.parent !== '' && (
          <button
            type="button"
            onClick={() => setPath(data.parent)}
            className="flex w-full items-center gap-2 px-2 py-1.5 text-left text-sm hover:bg-accent"
          >
            <FolderUp className="size-4 shrink-0 text-muted-foreground" />
            <span>..</span>
          </button>
        )}
        {isLoading ? (
          <p className="px-2 py-2 text-xs text-muted-foreground">{t('common.loading')}</p>
        ) : isError ? (
          <p className="px-2 py-2 text-xs text-destructive">{errMsg || t('artifactCache.browseFailed')}</p>
        ) : !data || data.dirs.length === 0 ? (
          <p className="px-2 py-2 text-xs text-muted-foreground">{t('artifactCache.browseEmpty')}</p>
        ) : (
          data.dirs.map((d) => (
            <button
              key={d.path}
              type="button"
              onClick={() => setPath(d.path)}
              className="flex w-full items-center gap-2 px-2 py-1.5 text-left text-sm hover:bg-accent"
            >
              <Folder className="size-4 shrink-0 text-muted-foreground" />
              <span className="truncate">{d.name}</span>
            </button>
          ))
        )}
      </div>

      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" size="sm" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <Button
          type="button"
          size="sm"
          disabled={!current}
          onClick={() => onPick(current)}
        >
          <Check className="size-3.5" />
          {t('artifactCache.browsePick')}
        </Button>
      </div>
    </div>
  )
}
