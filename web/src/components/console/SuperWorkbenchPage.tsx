import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import GridLayout, { WidthProvider, type Layout } from 'react-grid-layout'
import { LayoutGrid } from 'lucide-react'
import { useWorkspaceStore } from '@/stores/workspace'
import { GRID_COLS, GRID_ROW_HEIGHT, cardTypeDef } from '@/lib/workspace-card'
import { cardsToLayout, type PlacedCard } from '@/lib/workspace-preset'
import { parseDragPayload } from '@/lib/instance-library'
import { cn } from '@/lib/utils'
import WorkspaceCard from './WorkspaceCard'
import InstanceLibrary from './InstanceLibrary'
import SuperWorkbenchToolbar from './SuperWorkbenchToolbar'
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

export default function SuperWorkbenchPage() {
  const { t } = useTranslation()
  const [libraryCollapsed, setLibraryCollapsed] = useState(false)
  // 放置区高亮：dragover 计数（进入子元素会触发 leave，故用计数避免闪烁）。
  const [dropActive, setDropActive] = useState(false)
  const dragDepth = useRef(0)

  const ensureSuperCanvas = useWorkspaceStore((s) => s.ensureSuperCanvas)
  const canvas = useWorkspaceStore((s) => s.superCanvas)
  const userPresets = useWorkspaceStore((s) => s.userPresets)
  const applySuperPreset = useWorkspaceStore((s) => s.applySuperPreset)
  const dropToSuper = useWorkspaceStore((s) => s.dropToSuper)
  const removeSuperCard = useWorkspaceStore((s) => s.removeSuperCard)
  const updateSuperLayout = useWorkspaceStore((s) => s.updateSuperLayout)
  const setSuperFullscreen = useWorkspaceStore((s) => s.setSuperFullscreen)
  const saveSuperPresetAs = useWorkspaceStore((s) => s.saveSuperPresetAs)
  const deleteUserPreset = useWorkspaceStore((s) => s.deleteUserPreset)

  // 首次进入：惰性以空画布初始化。
  useEffect(() => {
    ensureSuperCanvas()
  }, [ensureSuperCanvas])

  const cards = canvas?.cards ?? EMPTY_CARDS
  const fullscreenId = canvas?.fullscreenCardId ?? null
  const layout = useMemo<Layout[]>(() => cardsToLayout(cards), [cards])

  const handleLayoutChange = useCallback(
    (next: Layout[]) => {
      updateSuperLayout(next.map((l) => ({ i: l.i, x: l.x, y: l.y, w: l.w, h: l.h })))
    },
    [updateSuperLayout],
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
      dropToSuper(payload)
      nudgeRelayout()
    }
  }

  return (
    <div className="flex h-full min-h-0">
      <InstanceLibrary collapsed={libraryCollapsed} onToggleCollapsed={() => setLibraryCollapsed((v) => !v)} />

      <div className="flex min-w-0 flex-1 flex-col">
        <SuperWorkbenchToolbar
          presetId={canvas?.presetId ?? 'super-empty'}
          userPresets={userPresets}
          onApplyPreset={applySuperPreset}
          onSavePreset={saveSuperPresetAs}
          onDeletePreset={deleteUserPreset}
        />

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
              <WorkspaceCard
                cardId={fullscreenCard.id}
                type={fullscreenCard.type}
                instanceId={fullscreenCard.instanceId ?? 0}
                fullscreen
                onToggleFullscreen={() => setSuperFullscreen(null)}
                onClose={() => removeSuperCard(fullscreenCard.id)}
              />
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
                    <WorkspaceCard
                      cardId={card.id}
                      type={card.type}
                      instanceId={card.instanceId ?? 0}
                      fullscreen={false}
                      onToggleFullscreen={() => setSuperFullscreen(card.id)}
                      onClose={() => removeSuperCard(card.id)}
                    />
                  </div>
                )
              })}
            </ResponsiveGrid>
          )}
        </div>
      </div>
    </div>
  )
}
