import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useInstances, type InstanceInfo } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useBotSummary } from '@/api/bots'
import { useConsoleStore } from '@/stores/console'
import { groupInstancesByNode } from './instance-tree'
import { groupInstances, type GroupDimension } from './instance-grouping'
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
  // 控制台树分组维度（FR-047）：默认按环境/状态聚合；选某节点时另含「无分组」平铺。
  const [dim, setDim] = useState<GroupDimension>('none')

  if (isLoading) {
    return <p className="px-3 py-2 text-xs text-muted-foreground">{t('common.loading')}</p>
  }

  if (!instances || instances.length === 0) {
    return <p className="px-3 py-2 text-xs text-muted-foreground">{t('console.noInstances')}</p>
  }

  const groupSelector = (
    <div className="flex items-center gap-1 px-2 pb-1">
      {(['none', 'env', 'status'] as const).map((d) => (
        <button
          key={d}
          type="button"
          onClick={() => setDim(d)}
          className={cn(
            'rounded px-1.5 py-0.5 text-[11px]',
            dim === d ? 'bg-accent font-medium text-foreground' : 'text-muted-foreground hover:bg-accent/50',
          )}
        >
          {t(`grouping.dim_${d}`)}
        </button>
      ))}
    </div>
  )

  // 选某节点时：dim=none 平铺；否则按所选维度（环境/状态）聚合。
  if (selectedNodeId !== null) {
    if (dim === 'none') {
      return (
        <div>
          {groupSelector}
          <ul className="space-y-0.5">
            {instances.map((inst) => (
              <InstanceRow key={inst.id} instance={inst} botBadge={badges.get(inst.id)} />
            ))}
          </ul>
        </div>
      )
    }
    const groups = groupInstances(instances, dim)
    return (
      <div>
        {groupSelector}
        <GenericGroups groups={groups} dim={dim} badges={badges} />
      </div>
    )
  }

  // 全部节点：dim=none 按节点分组（保留 host 名与孤儿处理）；否则按所选维度聚合。
  if (dim === 'none') {
    const groups = groupInstancesByNode(instances, nodes ?? [])
    return (
      <div>
        {groupSelector}
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
      </div>
    )
  }

  const groups = groupInstances(instances, dim)
  return (
    <div>
      {groupSelector}
      <GenericGroups groups={groups} dim={dim} badges={badges} />
    </div>
  )
}

/** 按通用维度（环境/状态）渲染分组小标题 + 成员行。空 key 显示「未分组」占位。 */
function GenericGroups({
  groups,
  dim,
  badges,
}: {
  groups: { key: string; instances: InstanceInfo[] }[]
  dim: GroupDimension
  badges: Map<number, BadgeData | undefined>
}) {
  const { t } = useTranslation()
  const label = (key: string): string => {
    if (key === '') return dim === 'env' ? t('grouping.envNone') : t('grouping.ungrouped')
    if (dim === 'env') return t(`grouping.env_${key}`, { defaultValue: key })
    return key
  }
  return (
    <div className="space-y-2">
      {groups.map((group) => (
        <div key={group.key || '__none__'}>
          <p className="px-3 py-1 text-xs font-medium text-muted-foreground">{label(group.key)}</p>
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
