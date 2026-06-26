import { memo } from 'react'
import { useTranslation } from 'react-i18next'
import { GripVertical, Maximize2, Minimize2, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { cardTypeDef, type CardType } from '@/lib/workspace-card'
import WorkspaceCardBody from './WorkspaceCardBody'

/**
 * 统一卡壳（FR-166）：grip 拖拽手柄 + 实例·功能标签 + 全屏 + 关闭，内嵌 {@link WorkspaceCardBody}。
 *
 * grip 是 react-grid-layout 的拖拽手柄（`.workspace-card-grip`，配 `draggableHandle`），
 * 这样卡片内部（终端/编辑器/表格）的交互不被拖拽吞掉——只有按住卡头 grip 才移动卡片。
 * `memo` 避免画布因其它卡布局变化而无谓重渲本卡（终端/WS 稳定）。
 */
interface WorkspaceCardProps {
  /** 卡片 id（react-grid-layout 的 key）。 */
  cardId: string
  /** 卡片功能类型。 */
  type: CardType
  /** 实例 id。 */
  instanceId: number
  /** 实例展示名（卡头副标题）。 */
  instanceName: string
  /** 是否处于全屏（最大化单卡）。 */
  fullscreen: boolean
  /** 切换全屏。 */
  onToggleFullscreen: () => void
  /** 关闭本卡。 */
  onClose: () => void
}

function WorkspaceCardImpl({
  cardId,
  type,
  instanceId,
  instanceName,
  fullscreen,
  onToggleFullscreen,
  onClose,
}: WorkspaceCardProps) {
  const { t } = useTranslation()
  const def = cardTypeDef(type)
  const title = def ? t(def.titleKey) : type

  return (
    <div
      data-card-id={cardId}
      className="flex h-full min-h-0 flex-col overflow-hidden rounded-xl border bg-card text-card-foreground shadow-soft"
    >
      <div className="flex shrink-0 items-center gap-1.5 border-b px-2 py-1.5">
        {/* grip 拖拽手柄：仅此处可发起拖拽（draggableHandle） */}
        <button
          type="button"
          aria-label={t('workspace.dragHandle')}
          title={t('workspace.dragHandle')}
          className={cn(
            'workspace-card-grip flex size-6 shrink-0 cursor-grab items-center justify-center rounded-md text-muted-foreground transition-colors hover:text-foreground active:cursor-grabbing',
            // 全屏时禁用拖拽（脱离网格）。
            fullscreen && 'pointer-events-none opacity-40',
          )}
          // 阻止全屏态下 grip 触发拖拽（RGL 在全屏分支不渲染，这里仅兜底视觉）。
          onClick={(e) => e.preventDefault()}
        >
          <GripVertical className="size-4" />
        </button>
        <div className="flex min-w-0 flex-1 items-baseline gap-1.5">
          <span className="truncate text-xs font-semibold tracking-wide text-foreground">{title}</span>
          <span className="truncate text-[11px] text-muted-foreground">{instanceName}</span>
        </div>
        <button
          type="button"
          aria-label={fullscreen ? t('workspace.exitFullscreen') : t('workspace.fullscreen')}
          title={fullscreen ? t('workspace.exitFullscreen') : t('workspace.fullscreen')}
          className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          onClick={onToggleFullscreen}
        >
          {fullscreen ? <Minimize2 className="size-3.5" /> : <Maximize2 className="size-3.5" />}
        </button>
        <button
          type="button"
          aria-label={t('common.close')}
          title={t('common.close')}
          className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
          onClick={onClose}
        >
          <X className="size-3.5" />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-hidden">
        <WorkspaceCardBody instanceId={instanceId} type={type} />
      </div>
    </div>
  )
}

/** 见 {@link WorkspaceCardProps}。 */
const WorkspaceCard = memo(WorkspaceCardImpl)
export default WorkspaceCard
