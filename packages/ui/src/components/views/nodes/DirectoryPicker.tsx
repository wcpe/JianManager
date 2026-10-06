import { useTranslation } from 'react-i18next'
import { Folder, FolderUp, RefreshCw, Check } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'

/** 目录浏览数据（本组件所需的最小结构；外壳传 API 返回项会结构兼容）。 */
export interface BrowseDirView {
  /** 服务端规范化后的当前路径。 */
  path?: string
  /** 上级路径（根/盘符列表时为空，此时不显示「..」）。 */
  parent?: string
  dirs: { name: string; path: string }[]
}

/**
 * 节点目录选择器（FR-178）：逐级浏览节点上的目录、选定一个绝对路径用于 JDK 登记。
 *
 * 受控视图（ADR-097 a 范式）：不取数——浏览数据经 props 注入。
 * 「当前路径」由外壳持有而非视图内部状态：它是查询键的一部分（换路径即换查询），
 * 视图只上报导航意图（`onNavigate`），这也让「刷新」与「回到上级」走同一条数据通路。
 */
export interface DirectoryPickerProps {
  /** 当前路径（无数据时用于显示）。 */
  path: string
  /** 浏览数据。 */
  data?: BrowseDirView
  isLoading?: boolean
  isError?: boolean
  /** 浏览失败的后端消息。 */
  errorMessage?: string
  /** 刷新在途（刷新按钮转圈）。 */
  isFetching?: boolean
  /** 进入某个目录（含上级）。 */
  onNavigate: (path: string) => void
  /** 重新拉取当前目录。 */
  onRefresh: () => void
  /** 选定当前目录（传回绝对路径）。 */
  onPick: (path: string) => void
  /** 取消/关闭。 */
  onCancel: () => void
}

/**
 * 目录选择器：内联在登记表单内的稳定子视图（不切换隐显致布局重组，符合抽屉 UX 约束）。
 * 顶部显示当前路径与「选定此目录」，列表逐级进入子目录、可回到上级。
 */
export default function DirectoryPicker({
  path,
  data,
  isLoading,
  isError,
  errorMessage,
  isFetching = false,
  onNavigate,
  onRefresh,
  onPick,
  onCancel,
}: DirectoryPickerProps) {
  const { t } = useTranslation()

  // 优先显示服务端规范化后的路径（相对输入可能被解析成绝对路径）。
  const current = data?.path ?? path

  return (
    <div className="rounded-md border bg-muted/30 p-3 space-y-2">
      <div className="flex items-center gap-2">
        <span className="text-xs text-muted-foreground shrink-0">{t('artifactCache.browseCurrent')}</span>
        <code className="flex-1 truncate rounded bg-background px-2 py-1 text-xs font-mono" title={current || '/'}>
          {current || t('artifactCache.browseRoots')}
        </code>
        <button
          type="button"
          onClick={onRefresh}
          disabled={isFetching}
          className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-60"
          title={t('common.refresh')}
        >
          <RefreshCw className={`size-3.5 ${isFetching ? 'animate-spin' : ''}`} />
        </button>
      </div>

      <div className="max-h-56 overflow-y-auto rounded border bg-background">
        {data?.parent !== undefined && data.parent !== '' && (
          <button
            type="button"
            onClick={() => onNavigate(data.parent!)}
            className="flex w-full items-center gap-2 px-2 py-1.5 text-left text-sm hover:bg-accent"
          >
            <FolderUp className="size-4 shrink-0 text-muted-foreground" />
            <span>..</span>
          </button>
        )}
        {isLoading ? (
          <p className="px-2 py-2 text-xs text-muted-foreground">{t('common.loading')}</p>
        ) : isError ? (
          <p className="px-2 py-2 text-xs text-destructive">{errorMessage || t('artifactCache.browseFailed')}</p>
        ) : !data || data.dirs.length === 0 ? (
          <p className="px-2 py-2 text-xs text-muted-foreground">{t('artifactCache.browseEmpty')}</p>
        ) : (
          data.dirs.map((d) => (
            <button
              key={d.path}
              type="button"
              onClick={() => onNavigate(d.path)}
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
