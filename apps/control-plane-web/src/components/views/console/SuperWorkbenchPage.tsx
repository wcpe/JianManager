import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import GridLayout, { WidthProvider, type Layout } from 'react-grid-layout'
import { LayoutGrid } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { GRID_COLS, GRID_ROW_HEIGHT, cardTypeDef } from '@/lib/workspace-card'
import { cardsToLayout } from '@/lib/workspace-preset'
import type { PlacedCard, WorkspacePreset } from '@/lib/workspace-preset'
import { parseDragPayload } from '@/lib/instance-library'
import type { DragPayload } from '@/lib/instance-library'
import 'react-grid-layout/css/styles.css'
import './workspace-canvas.css'

const ResponsiveGrid = WidthProvider(GridLayout)

/** 稳定空卡片数组引用（canvas 未就绪时复用，避免 hooks 依赖每帧变化）。 */
const EMPTY_CARDS: PlacedCard[] = []

/**
 * 跨实例超级工作台页面（FR-167 / 复用 ADR-034 可组合卡片工作区）。
 *
 * 左 = 可收起「实例库」拖拽源；右 = 跨实例画布（卡片携 instanceId，可并存多实例卡，如监看墙）。
 * 复用 FR-166 的卡壳 {@link WorkspaceCard}、网格、惰性挂载（未在画布的卡不建 WS）、预设机制；
 * 作用域为 store 的 `superCanvas`（与单实例 `canvasByInstance` 并存、清晰区分）。
 *
 * 添加卡片靠实例库拖拽：拖实例=默认卡组、拖功能=单卡、多选批量拖=监看墙；放置区高亮 + 松手落位。
 */

/** 派发 resize，让 xterm fit / 编辑器 relayout 按新尺寸重排（卡 resize/全屏切换后调用）。 */
function nudgeRelayout(): void {
  requestAnimationFrame(() => {
    window.dispatchEvent(new Event('resize'))
    requestAnimationFrame(() => window.dispatchEvent(new Event('resize')))
  })
}

/** 画布状态投影（应用侧 workspace store 的 `superCanvas`）。 */
export interface SuperCanvasState {
  cards: PlacedCard[]
  presetId?: string
  fullscreenCardId?: string | null
}

export interface SuperWorkbenchPageProps {
  /** 跨实例画布状态；未就绪（null）时按空画布渲染。 */
  canvas?: SuperCanvasState | null
  /** 可应用/删除的已保存预设。 */
  userPresets: WorkspacePreset[]
  /** 首次进入：惰性以空画布初始化。 */
  onEnsureCanvas: () => void
  onApplyPreset: (presetId: string) => void
  /** 实例库拖拽落位。 */
  onDropCard: (payload: DragPayload) => void
  onRemoveCard: (cardId: string) => void
  /** 网格布局变更（已裁剪为 i/x/y/w/h 五字段）。 */
  onLayoutChange: (layout: { i: string; x: number; y: number; w: number; h: number }[]) => void
  /** 全屏卡切换（null = 退出全屏）。 */
  onFullscreenChange: (cardId: string | null) => void
  onSavePresetAs: (name: string) => void
  onDeletePreset: (presetId: string) => void
  /** 渲染左侧实例库（应用侧接线层）。 */
  renderLibrary: (args: { collapsed: boolean; onToggleCollapsed: () => void }) => ReactNode
  /** 渲染顶部工具栏（应用侧接线层）。 */
  renderToolbar: (args: {
    presetId: string
    userPresets: WorkspacePreset[]
    onApplyPreset: (presetId: string) => void
    onSavePreset: (name: string) => void
    onDeletePreset: (presetId: string) => void
    onOpenFocusTerminals: () => void
    focusTerminalsDisabledReason?: string
  }) => ReactNode
  /** 渲染单张卡（应用侧接线层 WorkspaceCard）。 */
  renderCard: (args: {
    card: PlacedCard
    fullscreen?: boolean
    onToggleFullscreen: () => void
    onClose: () => void
  }) => ReactNode
  /**
   * 渲染「专注终端」沉浸台（应用侧接线层组件，自带实例名册/指标/回溯等取数）。
   * 只在 focusInstanceId 非空时调用。
   */
  renderFocusTerminals: (args: { initialInstanceId: number; onExit: () => void }) => ReactNode
}

export function SuperWorkbenchPage({
  canvas,
  userPresets,
  onEnsureCanvas,
  onApplyPreset,
  onDropCard,
  onRemoveCard,
  onLayoutChange,
  onFullscreenChange,
  onSavePresetAs,
  onDeletePreset,
  renderLibrary,
  renderToolbar,
  renderCard,
  renderFocusTerminals,
}: SuperWorkbenchPageProps) {
  const { t } = useTranslation()
  const [libraryCollapsed, setLibraryCollapsed] = useState(false)
  // 放置区高亮：dragover 计数（进入子元素会触发 leave，故用计数避免闪烁）。
  const [dropActive, setDropActive] = useState(false)
  const dragDepth = useRef(0)

  // 画布状态与全部动作由 props 注入（ADR-097）：本视图不触达 store。

  // 首次进入：惰性以空画布初始化。
  useEffect(() => {
    onEnsureCanvas()
  }, [onEnsureCanvas])

  const cards = canvas?.cards ?? EMPTY_CARDS
  // 专注终端（融合入口）：画布上首个终端卡的实例作为沉浸台起点；无终端卡则按钮禁用提示。
  const firstTerminalInstanceId = cards.find((card) => card.type === 'terminal')?.instanceId ?? null
  const [focusInstanceId, setFocusInstanceId] = useState<number | null>(null)
  const fullscreenId = canvas?.fullscreenCardId ?? null
  const layout = useMemo<Layout[]>(() => cardsToLayout(cards), [cards])

  const handleLayoutChange = useCallback(
    (next: Layout[]) => {
      onLayoutChange(next.map((l) => ({ i: l.i, x: l.x, y: l.y, w: l.w, h: l.h })))
    },
    [onLayoutChange],
  )

  const fullscreenCard = fullscreenId ? cards.find((c) => c.id === fullscreenId) : undefined

  // 全屏切换后让内部面板重排。
  const prevFullscreen = useRef<string | null>(null)
  useEffect(() => {
    if (prevFullscreen.current !== fullscreenId) {
      prevFullscreen.current = fullscreenId
      nudgeRelayout()
    }
  }, [fullscreenId])

  const resetDrag = () => {
    dragDepth.current = 0
    setDropActive(false)
  }

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault()
    resetDrag()
    const payload = parseDragPayload(
      e.dataTransfer.getData('application/x-jm-workspace') || e.dataTransfer.getData('text/plain'),
    )
    if (payload) {
      onDropCard(payload)
      nudgeRelayout()
    }
  }

  return (
    <div className="flex h-full min-h-0">
      {renderLibrary({ collapsed: libraryCollapsed, onToggleCollapsed: () => setLibraryCollapsed((v) => !v) })}

      <div className="flex min-w-0 flex-1 flex-col">
        {renderToolbar({
          presetId: canvas?.presetId ?? 'super-empty',
          userPresets,
          onApplyPreset,
          onSavePreset: onSavePresetAs,
          onDeletePreset,
          onOpenFocusTerminals: () => {
            if (firstTerminalInstanceId != null) setFocusInstanceId(firstTerminalInstanceId)
          },
          focusTerminalsDisabledReason:
            firstTerminalInstanceId == null ? t('superWorkbench.focusTerminalsEmpty') : undefined,
        })}

        {/* 放置区：整块画布是落点；dragover 高亮（主色虚线，复用 token）。 */}
        <div
          className={cn(
            'relative min-h-0 flex-1 overflow-auto transition-colors',
            dropActive && 'bg-primary/[0.04] ring-2 ring-inset ring-primary/40',
          )}
          onDragEnter={(e) => {
            e.preventDefault()
            dragDepth.current += 1
            setDropActive(true)
          }}
          onDragOver={(e) => {
            e.preventDefault()
            e.dataTransfer.dropEffect = 'copy'
          }}
          onDragLeave={() => {
            dragDepth.current -= 1
            if (dragDepth.current <= 0) resetDrag()
          }}
          onDrop={handleDrop}
        >
          {fullscreenCard ? (
            <div className="h-full p-3">
              {renderCard({
                card: fullscreenCard,
                fullscreen: true,
                onToggleFullscreen: () => onFullscreenChange(null),
                onClose: () => onRemoveCard(fullscreenCard.id),
              })}
            </div>
          ) : cards.length === 0 ? (
            <div className="pointer-events-none flex h-full flex-col items-center justify-center gap-2 text-center">
              <LayoutGrid className="size-8 text-muted-foreground/40" />
              <p className="text-sm font-medium text-muted-foreground">{t('superWorkbench.emptyCanvas')}</p>
              <p className="max-w-sm text-xs text-muted-foreground/70">{t('superWorkbench.emptyCanvasHint')}</p>
            </div>
          ) : (
            <ResponsiveGrid
              className="layout"
              layout={layout}
              cols={GRID_COLS}
              rowHeight={GRID_ROW_HEIGHT}
              margin={[12, 12]}
              containerPadding={[12, 12]}
              draggableHandle=".workspace-card-grip"
              draggableCancel=".workspace-card-grip button"
              compactType="vertical"
              onLayoutChange={handleLayoutChange}
              onResizeStop={nudgeRelayout}
              onDragStop={nudgeRelayout}
              resizeHandles={['se']}
            >
              {cards.map((card) => {
                const def = cardTypeDef(card.type)
                return (
                  <div key={card.id} data-grid-min-w={def?.minSize.w} className="min-h-0">
                    {renderCard({
                      card,
                      onToggleFullscreen: () => onFullscreenChange(card.id),
                      onClose: () => onRemoveCard(card.id),
                    })}
                  </div>
                )
              })}
            </ResponsiveGrid>
          )}
        </div>
      </div>

      {/* 专注终端（ADR-087 沉浸台）：从工作台画布进入的纯终端全屏模式；退出回本页。
          pane 渲染与会话保活由应用侧接线层负责（注入 renderFocusTerminals）。 */}
      {focusInstanceId != null &&
        renderFocusTerminals({ initialInstanceId: focusInstanceId, onExit: () => setFocusInstanceId(null) })}
    </div>
  )
}