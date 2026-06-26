import { memo, useMemo } from 'react'
import GridLayout, { WidthProvider, type Layout } from 'react-grid-layout'
import { GRID_COLS, GRID_ROW_HEIGHT, cardTypeDef } from '@/lib/workspace-card'
import { cardsToLayout, type PlacedCard } from '@/lib/workspace-preset'
import { DirectorRenderProvider } from '@/lib/director-render'
import { cn } from '@/lib/utils'
import WorkspaceCard from './WorkspaceCard'
import 'react-grid-layout/css/styles.css'
import './workspace-canvas.css'

const ResponsiveGrid = WidthProvider(GridLayout)

/**
 * 导播台单场景画布（FR-168 / ADR-035）。
 *
 * 渲染一个场景的卡片网格（复用 FR-166 卡壳 {@link WorkspaceCard}）。与可编辑画布不同：
 * - **只读布局**：导播台聚焦瞬切/轮播，场景布局在超级工作台预先编排好，这里不拖拽/不缩放/不关卡。
 * - **保活 + 节流**：本组件**始终挂载**（故卡内 WS 保活）；非激活场景由 `active=false` 触发节流——
 *   ① 通过 {@link DirectorRenderProvider} 让终端暂停 xterm 重绘（WS 继续收数据缓冲）；
 *   ② 容器 `content-visibility:hidden`（CSS 类 `director-scene-idle`）让浏览器跳过整棵子树的布局/绘制
 *      （图表/DOM 不重绘），切回激活即恢复。这正是 ADR-035 的「非激活降频/暂停渲染」。
 *
 * 同一时刻只有 active 场景可见（绝对定位铺满，非激活叠在下层且 `pointer-events-none`）。
 */
interface DirectorCanvasProps {
  /** 场景卡片快照（携 instanceId）。 */
  cards: PlacedCard[]
  /** 是否为当前激活场景（全速渲染 + 可见 + 可交互）。 */
  active: boolean
}

function DirectorCanvasImpl({ cards, active }: DirectorCanvasProps) {
  const layout = useMemo<Layout[]>(() => cardsToLayout(cards), [cards])

  return (
    <DirectorRenderProvider value={{ active }}>
      <div
        // 绝对铺满：所有预热场景叠放同一区域，仅 active 显示在最上层且可交互。
        className={cn(
          'absolute inset-0 overflow-auto',
          active ? 'z-10 opacity-100' : 'director-scene-idle pointer-events-none z-0 opacity-0',
        )}
        aria-hidden={!active}
        // 非激活：跳过整棵子树的渲染（图表/DOM 不重绘），但保持挂载以保活 WS。
        style={active ? undefined : { contentVisibility: 'hidden' }}
      >
        <ResponsiveGrid
          className="layout"
          layout={layout}
          cols={GRID_COLS}
          rowHeight={GRID_ROW_HEIGHT}
          margin={[12, 12]}
          containerPadding={[12, 12]}
          // 导播台只读：禁用一切拖拽/缩放（无手柄、无 resize handle）。
          isDraggable={false}
          isResizable={false}
          compactType="vertical"
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
                  // 导播台不在卡级全屏/关卡（场景级切换）；提供 no-op 满足卡壳接口。
                  onToggleFullscreen={NOOP}
                  onClose={NOOP}
                  readOnly
                />
              </div>
            )
          })}
        </ResponsiveGrid>
      </div>
    </DirectorRenderProvider>
  )
}

const NOOP = () => {}

/** 见 {@link DirectorCanvasProps}。`memo` 避免非激活场景因其它场景切换而无谓重渲。 */
const DirectorCanvas = memo(DirectorCanvasImpl)
export default DirectorCanvas
