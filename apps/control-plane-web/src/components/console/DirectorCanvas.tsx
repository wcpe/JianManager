// 视图已迁至 @jianmanager/ui（ADR-097）；本层只注入卡壳（受控化）。
import { DirectorCanvas as DirectorCanvasView } from '@jianmanager/ui/components/views/console/DirectorCanvas'
import type { PlacedCard } from '@/lib/workspace-preset'
import WorkspaceCard from './WorkspaceCard'

interface DirectorCanvasProps {
  /** 场景卡片快照（携 instanceId）。 */
  cards: PlacedCard[]
  /** 是否为当前激活场景（全速渲染 + 可见 + 可交互）。 */
  active: boolean
}

/**
 * 导播台单场景画布的接线层（FR-168 / ADR-035）。
 *
 * 画布布局与保活/节流策略已入包；卡壳 `WorkspaceCard`（含实例取数与 WS 保活）留应用侧，
 * 经 renderCard 注入。导播台不在卡级全屏/关卡（场景级切换），故传 no-op 满足卡壳接口。
 */
export default function DirectorCanvas({ cards, active }: DirectorCanvasProps) {
  return (
    <DirectorCanvasView
      cards={cards}
      active={active}
      renderCard={({ card }) => (
        <WorkspaceCard
          cardId={card.id}
          type={card.type}
          instanceId={card.instanceId ?? 0}
          fullscreen={false}
          onToggleFullscreen={NOOP}
          onClose={NOOP}
          readOnly
        />
      )}
    />
  )
}

const NOOP = () => {}
