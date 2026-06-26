import { useCallback, useEffect, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import GridLayout, { WidthProvider, type Layout } from 'react-grid-layout'
import {
  useInstance,
  useStartInstance,
  useStopInstance,
  useRestartInstance,
  useKillInstance,
} from '@/api/instances'
import { useConsoleStore } from '@/stores/console'
import { useWorkspaceStore } from '@/stores/workspace'
import { GRID_COLS, GRID_ROW_HEIGHT, cardTypeDef, type CardType } from '@/lib/workspace-card'
import { cardsToLayout, type PlacedCard } from '@/lib/workspace-preset'
import WorkspaceCard from './WorkspaceCard'
import WorkspaceToolbar from './WorkspaceToolbar'
import 'react-grid-layout/css/styles.css'
import './workspace-canvas.css'

const ResponsiveGrid = WidthProvider(GridLayout)

/** 稳定的空卡片数组引用（canvas 未就绪时复用，避免 hooks 依赖每帧变化）。 */
const EMPTY_CARDS: PlacedCard[] = []

/**
 * 可组合卡片工作区画布（FR-166 / ADR「可组合卡片工作区」取代 ADR-030）。
 *
 * 取代固定六 Tab 的 `WorkspacePane`：实例工作区 = 可拖拽网格画布，卡片 = 实例 × 功能，
 * 自由摆放 / 调大小 / 不重叠流式布局；命名预设保存布局（个人级 localStorage）。
 *
 * - **惰性挂载**：只渲染当前画布上的卡片 → 仅这些卡建立 WS / metrics 轮询（承 ADR）。
 * - **拖拽手柄**：`draggableHandle=".workspace-card-grip"`，卡内交互（终端/编辑器）不被吞。
 * - **卡 resize → relayout**：拖拽/缩放结束后派发 window resize，触发终端 fit、编辑器 relayout。
 * - **全屏**：临时最大化单卡（脱离网格，铺满工作区），再次点击还原。
 */
interface WorkspaceCanvasProps {
  /** 当前工作区打开的实例 id（本 FR 单实例）。 */
  instanceId: number
}

/** 派发一个 resize 事件，让 xterm fit / CodeMirror 等按新尺寸重排（卡 resize/全屏切换后调用）。 */
function nudgeRelayout(): void {
  // 双帧：等 RGL/全屏的 DOM 尺寸落定后再 fit。
  requestAnimationFrame(() => {
    window.dispatchEvent(new Event('resize'))
    requestAnimationFrame(() => window.dispatchEvent(new Event('resize')))
  })
}

export default function WorkspaceCanvas({ instanceId }: WorkspaceCanvasProps) {
  const { t } = useTranslation()
  const { data: instance } = useInstance(instanceId)
  const closeInstance = useConsoleStore((s) => s.closeInstance)

  const start = useStartInstance()
  const stop = useStopInstance()
  const restart = useRestartInstance()
  const kill = useKillInstance()

  const ensureCanvas = useWorkspaceStore((s) => s.ensureCanvas)
  const canvas = useWorkspaceStore((s) => s.canvasByInstance[instanceId])
  const userPresets = useWorkspaceStore((s) => s.userPresets)
  const applyPreset = useWorkspaceStore((s) => s.applyPreset)
  const addCard = useWorkspaceStore((s) => s.addCard)
  const removeCard = useWorkspaceStore((s) => s.removeCard)
  const updateLayout = useWorkspaceStore((s) => s.updateLayout)
  const setFullscreen = useWorkspaceStore((s) => s.setFullscreen)
  const savePresetAs = useWorkspaceStore((s) => s.savePresetAs)
  const deleteUserPreset = useWorkspaceStore((s) => s.deleteUserPreset)

  // 首次打开实例：惰性以默认「运维台」初始化画布。
  useEffect(() => {
    ensureCanvas(instanceId)
  }, [instanceId, ensureCanvas])

  // 稳定空数组引用：canvas 未就绪时不每帧新建数组，避免 useMemo 依赖抖动。
  const cards = canvas?.cards ?? EMPTY_CARDS
  const fullscreenId = canvas?.fullscreenCardId ?? null
  const instanceName = instance?.name ?? `#${instanceId}`

  const layout = useMemo<Layout[]>(() => cardsToLayout(cards), [cards])

  const handleLayoutChange = useCallback(
    (next: Layout[]) => {
      updateLayout(
        instanceId,
        next.map((l) => ({ i: l.i, x: l.x, y: l.y, w: l.w, h: l.h })),
      )
    },
    [instanceId, updateLayout],
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

  return (
    <div className="flex h-full flex-col">
      <WorkspaceToolbar
        instanceId={instanceId}
        instanceName={instanceName}
        status={instance?.status ?? ''}
        presetId={canvas?.presetId ?? 'ops'}
        userPresets={userPresets}
        hasInstance={!!instance}
        startPending={start.isPending}
        stopPending={stop.isPending}
        restartPending={restart.isPending}
        killPending={kill.isPending}
        onStart={() => start.mutate(instanceId)}
        onStop={() => stop.mutate(instanceId)}
        onRestart={() => restart.mutate(instanceId)}
        onKill={() => kill.mutate(instanceId)}
        onApplyPreset={(id) => applyPreset(instanceId, id)}
        onAddCard={(type: CardType) => addCard(instanceId, type)}
        onSavePreset={(name) => savePresetAs(instanceId, name)}
        onDeletePreset={deleteUserPreset}
        onClose={closeInstance}
      />

      <div className="min-h-0 flex-1 overflow-auto">
        {fullscreenCard ? (
          // 全屏：单卡铺满工作区，脱离网格。
          <div className="h-full p-3">
            <WorkspaceCard
              cardId={fullscreenCard.id}
              type={fullscreenCard.type}
              instanceId={instanceId}
              instanceName={instanceName}
              fullscreen
              onToggleFullscreen={() => setFullscreen(instanceId, null)}
              onClose={() => removeCard(instanceId, fullscreenCard.id)}
            />
          </div>
        ) : cards.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center gap-2 text-center">
            <p className="text-sm font-medium text-muted-foreground">{t('workspace.emptyCanvas')}</p>
            <p className="text-xs text-muted-foreground/70">{t('workspace.emptyCanvasHint')}</p>
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
                    instanceId={instanceId}
                    instanceName={instanceName}
                    fullscreen={false}
                    onToggleFullscreen={() => setFullscreen(instanceId, card.id)}
                    onClose={() => removeCard(instanceId, card.id)}
                  />
                </div>
              )
            })}
          </ResponsiveGrid>
        )}
      </div>
    </div>
  )
}
