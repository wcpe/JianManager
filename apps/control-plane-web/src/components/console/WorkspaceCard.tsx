import { memo } from 'react'
import { useTranslation } from 'react-i18next'
import { useInstance } from '@/api/instances'
import { cardTypeDef, type CardType } from '@/lib/workspace-card'
import WorkspaceCardView from '@/components/views/instances/WorkspaceCard'
import WorkspaceCardBody from './WorkspaceCardBody'

/**
 * 统一卡壳的应用接线层（ADR-097）。
 *
 * 卡壳本体是受控视图（见 components/views）；本层提供三样它不该自己做的事：按卡类型解析标题、
 * 解析实例名（仅在父级未传名字时查，单实例画布零额外请求）、注入卡内容。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
interface WorkspaceCardProps {
  /** 卡片 id（react-grid-layout 的 key）。 */
  cardId: string
  /** 卡片功能类型。 */
  type: CardType
  /** 实例 id。 */
  instanceId: number
  /**
   * 实例展示名（卡头副标题）。单实例画布由父级统一传入（避免每卡各拉一次）；
   * 超级工作台省略此 prop，本层按 instanceId 自解析（每卡可属不同实例，FR-167）。
   */
  instanceName?: string
  /** 是否处于全屏（最大化单卡）。 */
  fullscreen: boolean
  /** 切换全屏。 */
  onToggleFullscreen: () => void
  /** 关闭本卡。 */
  onClose: () => void
  /** 只读卡壳（FR-168 导播台）。 */
  readOnly?: boolean
}

function WorkspaceCardImpl({
  cardId,
  type,
  instanceId,
  instanceName,
  fullscreen,
  onToggleFullscreen,
  onClose,
  readOnly = false,
}: WorkspaceCardProps) {
  const { t } = useTranslation()
  const def = cardTypeDef(type)
  const title = def ? t(def.titleKey) : type
  // 超级工作台未传 instanceName 时按 id 自解析（每卡可属不同实例）；
  // 传了名字则跳过查询（enabled=false），单实例画布零额外请求。
  const { data: selfInstance } = useInstance(instanceName === undefined ? instanceId : 0)
  const shownName = instanceName ?? selfInstance?.name ?? `#${instanceId}`

  return (
    <WorkspaceCardView
      cardId={cardId}
      title={title}
      instanceName={shownName}
      fullscreen={fullscreen}
      onToggleFullscreen={onToggleFullscreen}
      onClose={onClose}
      readOnly={readOnly}
      bodySlot={<WorkspaceCardBody instanceId={instanceId} type={type} />}
    />
  )
}

/** 见 {@link WorkspaceCardProps}。 */
const WorkspaceCard = memo(WorkspaceCardImpl)
export default WorkspaceCard
