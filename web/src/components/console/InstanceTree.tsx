import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { useInstances, type InstanceInfo } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useBotSummary } from '@/api/bots'
import { useConsoleStore } from '@/stores/console'
import { groupInstancesByNode } from './instance-tree'
import { indexBotBadgesByInstance, type InstanceBotBadge as BadgeData } from './bot-list'
import InstanceStatusDot from './InstanceStatusDot'
import InstanceBotBadge from './InstanceBotBadge'
import { cn } from '@/lib/utils'

/**
 * 常驻实例树。
 * 「全部节点」(selectedNodeId=null) → 拉全部实例并按节点分组；
 * 选某节点 → `GET /instances?nodeId=` 只列该节点实例（后端过滤）。
 * 点实例在工作区打开其工作面板（终端 | Bot）。
 * 每行挂 Bot 聚合徽标，数据来自单次 `GET /bots/summary?groupBy=instance`（FR-039，永不逐个 Bot）。
 */
export default function InstanceTree() {
  const { t } = useTranslation()
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)
  const { data: nodes } = useNodes()
  const { data: instances, isLoading } = useInstances(
    selectedNodeId === null ? undefined : { nodeId: selectedNodeId },
  )
  // 单次聚合摘要覆盖当前可见的实例集（与实例列表同 nodeId 过滤）
  const { data: botSummary } = useBotSummary({
    groupBy: 'instance',
    ...(selectedNodeId === null ? {} : { nodeId: selectedNodeId }),
  })
  const badges = useMemo(() => indexBotBadgesByInstance(botSummary?.groups), [botSummary])

  if (isLoading) {
    return <p className="px-3 py-2 text-xs text-muted-foreground">{t('common.loading')}</p>
  }

  if (!instances || instances.length === 0) {
    return <p className="px-3 py-2 text-xs text-muted-foreground">{t('console.noInstances')}</p>
  }

  // 选某节点时平铺；全部节点时按节点分组（含 host 名做小标题）
  if (selectedNodeId !== null) {
    return (
      <ul className="space-y-0.5">
        {instances.map((inst) => (
          <InstanceRow key={inst.id} instance={inst} botBadge={badges.get(inst.id)} />
        ))}
      </ul>
    )
  }

  const groups = groupInstancesByNode(instances, nodes ?? [])
  return (
    <div className="space-y-2">
      {groups
        .filter((g) => g.instances.length > 0)
        .map((group) => (
          <div key={group.nodeId}>
            <p className="px-3 py-1 text-xs font-medium text-muted-foreground">
              {group.nodeName ?? t('console.unknownNode', { id: group.nodeId })}
            </p>
            <ul className="space-y-0.5">
              {group.instances.map((inst) => (
                <InstanceRow key={inst.id} instance={inst} botBadge={badges.get(inst.id)} />
              ))}
            </ul>
          </div>
        ))}
    </div>
  )
}

function InstanceRow({ instance, botBadge }: { instance: InstanceInfo; botBadge: BadgeData | undefined }) {
  const openInstanceId = useConsoleStore((s) => s.openInstanceId)
  const openInstance = useConsoleStore((s) => s.openInstance)
  const isActive = openInstanceId === instance.id

  return (
    <li>
      <button
        type="button"
        onClick={() => openInstance(instance.id)}
        className={cn(
          'flex w-full items-center gap-2 rounded-md px-3 py-1.5 text-left text-sm',
          isActive ? 'bg-accent font-medium' : 'hover:bg-accent/50',
        )}
      >
        <InstanceStatusDot status={instance.status} />
        <span className="min-w-0 flex-1 truncate">{instance.name}</span>
        <InstanceBotBadge badge={botBadge} />
      </button>
    </li>
  )
}
