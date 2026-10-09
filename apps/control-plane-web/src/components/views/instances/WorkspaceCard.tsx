import { memo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { GripVertical, Maximize2, Minimize2, X } from 'lucide-react'
import { cn } from '@jianmanager/ui'

/**
 * 统一卡壳（FR-166）：grip 拖拽手柄 + 实例·功能标签 + 全屏 + 关闭，内嵌卡内容。
 *
 * grip 是 react-grid-layout 的拖拽手柄（`.workspace-card-grip`，配 `draggableHandle`），
 * 这样卡片内部（终端/编辑器/表格）的交互不被拖拽吞掉——只有按住卡头 grip 才移动卡片。
 * `memo` 避免画布因其它卡布局变化而无谓重渲本卡（终端/WS 稳定）。
 *
 * 受控视图（ADR-097）：
 * - 卡头标题（功能名）与副标题（实例名）由外壳解析后注入——标题需查卡类型注册表、
 *   实例名需查实例，两者都是外壳的取数/查表职责；
 * - 卡内容经 `bodySlot` 注入（它自带取数，本组件不该认识它）；
 * - 拖拽手柄、全屏、关闭仍是纯 UI 契约，原样保留。
 */
export interface WorkspaceCardProps {
  /** 卡片 id（react-grid-layout 的 key，写在 `data-card-id` 上）。 */
  cardId: string
  /** 卡头标题：功能名（外壳按卡类型解析后注入）。 */
  title: string
  /**
   * 卡头副标题：实例展示名。
   * 单实例画布由父级统一传入（避免每卡各拉一次）；超级工作台每卡可属不同实例，
   * 由外壳按各自 instanceId 解析后注入。
   */
  instanceName: string
  /** 是否处于全屏（最大化单卡）。 */
  fullscreen: boolean
  /** 切换全屏。 */
  onToggleFullscreen: () => void
  /** 关闭本卡。 */
  onClose: () => void
  /**
   * 只读卡壳（FR-168 导播台）：隐藏 grip 拖拽手柄 + 全屏 + 关闭按钮，卡头仅显标题。
   * 导播台场景为预编排只读视图，瞬切/轮播在场景级，不在卡级编辑。默认 false（FR-166/167 不变）。
   */
  readOnly?: boolean
  /** 卡内容（外壳注入，自带取数）。 */
  bodySlot?: ReactNode
}

function WorkspaceCardImpl({
  cardId,
  title,
  instanceName,
  fullscreen,
  onToggleFullscreen,
  onClose,
  readOnly = false,
  bodySlot,
}: WorkspaceCardProps) {
  const { t } = useTranslation()

  return (
    <div
      data-card-id={cardId}
      className="flex h-full min-h-0 flex-col overflow-hidden rounded-xl border bg-card text-card-foreground shadow-soft"
    >
      <div className="flex shrink-0 items-center gap-1.5 border-b px-2 py-1.5">
        {/* grip 拖拽手柄：仅此处可发起拖拽（draggableHandle）；只读卡（导播台）不显。 */}
        {!readOnly && (
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
        )}
        <div className={cn('flex min-w-0 flex-1 items-baseline gap-1.5', readOnly && 'pl-1')}>
          <span className="truncate text-xs font-semibold tracking-wide text-foreground">{title}</span>
          <span className="truncate text-[11px] text-muted-foreground">{instanceName}</span>
        </div>
        {!readOnly && (
          <>
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
          </>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-hidden">
        {bodySlot}
      </div>
    </div>
  )
}

/** 见 {@link WorkspaceCardProps}。 */
const WorkspaceCard = memo(WorkspaceCardImpl)
export default WorkspaceCard
