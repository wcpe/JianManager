import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useInstances, type InstanceInfo } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useBotSummary } from '@/api/bots'
import { useConsoleStore } from '@/stores/console'
import { groupInstancesByNode, toTreeBranches, type TreeBranch as Branch } from './instance-tree'
import { groupInstances, type GroupDimension } from './instance-grouping'
import { indexBotBadgesByInstance, type InstanceBotBadge as BadgeData } from './bot-list'
import InstanceStatusDot from './InstanceStatusDot'
import InstanceBotBadge from './InstanceBotBadge'
import { cn } from '@/lib/utils'

/**
 * 常驻实例树（FR-069 树形化）。
 * 「全部节点」(selectedNodeId=null) → 拉全部实例并按节点分组；
 * 选某节点 → `GET /instances?nodeId=` 只列该节点实例（后端过滤）。
 * 每个分组是一条可折叠分支（节点/环境/状态层级），折叠态记忆在 console store 的
 * collapsedGroups（键 `tree:<dim>:<group>`，与导航组 key 隔离）；折叠优先——折叠的分支
 * 不渲染成员行，故大量实例下不全量铺开、不卡。点实例在工作区打开其工作面板（终端 | Bot）。
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
  // 控制台树分组维度（FR-047/FR-069）：默认按节点分组；可切换环境/状态层级。
  const [dim, setDim] = useState<GroupDimension>('node')

  // 分组 → 带折叠键的树分支：全部节点视图保留节点名与孤儿处理，其余维度走通用分组。
  const branches = useMemo<{ branch: Branch; label: string }[]>(() => {
    if (!instances) return []
    if (dim === 'node') {
      const groups = groupInstancesByNode(instances, nodes ?? [])
        .filter((g) => g.instances.length > 0)
        .map((g) => ({
          key: String(g.nodeId),
          instances: g.instances,
          label: g.nodeName ?? t('console.unknownNode', { id: g.nodeId }),
        }))
      return toTreeBranches('node', groups).map((branch, i) => ({ branch, label: groups[i].label }))
    }
    const groups = groupInstances(instances, dim)
    return toTreeBranches(dim, groups).map((branch) => ({
      branch,
      label: dimLabel(dim, branch.key, t),
    }))
  }, [instances, nodes, dim, t])

  if (isLoading) {
    return <p className="px-3 py-2 text-xs text-muted-foreground">{t('common.loading')}</p>
  }

  if (!instances || instances.length === 0) {
    return <p className="px-3 py-2 text-xs text-muted-foreground">{t('console.noInstances')}</p>
  }

  return (
    <div>
      <div className="flex items-center gap-1 px-2 pb-1" role="group" aria-label={t('grouping.groupBy')}>
        {(['node', 'env', 'status'] as const).map((d) => (
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
      <div className="space-y-0.5">
        {branches.map(({ branch, label }) => (
          <TreeBranch key={branch.branchKey} branch={branch} label={label} badges={badges} />
        ))}
      </div>
    </div>
  )
}

/** 通用维度（环境/状态）分组标签。空 key 显示「未分组/未分环境」占位。 */
function dimLabel(dim: GroupDimension, key: string, t: (k: string, o?: Record<string, unknown>) => string): string {
  if (key === '') return dim === 'env' ? t('grouping.envNone') : t('grouping.ungrouped')
  if (dim === 'env') return t(`grouping.env_${key}`, { defaultValue: key })
  return key
}

/**
 * 一条可折叠树分支：头部（折叠箭头 + 标签 + 计数），展开时渲染成员行。
 * 折叠态来自 console store collapsedGroups[branchKey]，默认展开；折叠时不渲染成员（折叠优先）。
 */
function TreeBranch({
  branch,
  label,
  badges,
}: {
  branch: Branch
  label: string
  badges: Map<number, BadgeData | undefined>
}) {
  const collapsed = useConsoleStore((s) => s.collapsedGroups[branch.branchKey])
  const toggleGroup = useConsoleStore((s) => s.toggleGroup)

  return (
    <div>
      <button
        type="button"
        onClick={() => toggleGroup(branch.branchKey)}
        aria-expanded={!collapsed}
        className="flex w-full items-center gap-1 rounded px-2 py-1 text-left text-xs font-medium text-muted-foreground hover:bg-accent/50"
      >
        {collapsed ? (
          <ChevronRight className="size-3.5 shrink-0 opacity-70" />
        ) : (
          <ChevronDown className="size-3.5 shrink-0 opacity-70" />
        )}
        <span className="min-w-0 flex-1 truncate">{label}</span>
        <span className="shrink-0 tabular-nums opacity-60">{branch.instances.length}</span>
      </button>

      {!collapsed && (
        <ul className="space-y-0.5">
          {branch.instances.map((inst) => (
            <InstanceRow key={inst.id} instance={inst} botBadge={badges.get(inst.id)} />
          ))}
        </ul>
      )}
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
          'flex w-full items-center gap-2 rounded-md py-1.5 pl-7 pr-3 text-left text-sm',
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
