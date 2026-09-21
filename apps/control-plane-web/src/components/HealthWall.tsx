import { useState } from 'react'
import { Link } from 'react-router'
import { Panel } from '@jianmanager/ui/components/panel'
import { cn } from '@jianmanager/ui'
import { useHealthWall, type HealthLevel, type HealthWallNode, type HealthWallSort } from '@/api/metrics'
import {
  HEALTH_WALL_SORTS,
  formatPct,
  healthLevelTone,
  healthLevelLabel,
  healthWallSortLabel,
  summarizeHealthWall,
} from '@/lib/health-wall'
import { toneChipClass } from '@/lib/tone'

/** 分级 → 单元格着色（半透明状态底 + 状态前景，高密度热力墙用）。 */
const CELL_CLASS: Record<HealthLevel, string> = {
  offline: 'bg-status-danger/20 hover:bg-status-danger/30',
  stale: 'bg-status-info/20 hover:bg-status-info/30',
  degraded: 'bg-status-warning/20 hover:bg-status-warning/30',
  healthy: 'bg-status-success/15 hover:bg-status-success/25',
}

/** 单节点 tooltip：展开明细。 */
function cellTitle(node: HealthWallNode): string {
  const lines = [
    `${node.name}（${healthLevelLabel(node.level)}）`,
    `鲜度：${node.freshness}`,
    `CPU ${formatPct(node.cpuPct)} · 内存 ${formatPct(node.memPct)} · 磁盘 ${formatPct(node.diskPct)}`,
    `实例 运行 ${node.running} / 崩溃 ${node.crashed} / 停止 ${node.stopped}`,
    `活跃告警 ${node.activeAlerts}`,
  ]
  if (node.zone) lines.splice(1, 0, `分组：${node.zone}`)
  return lines.join('\n')
}

/** 单个健康格：着色表征分级，点击一键下钻单台监控。 */
function HealthCell({ node }: { node: HealthWallNode }) {
  return (
    <Link
      to={node.href}
      title={cellTitle(node)}
      aria-label={`${node.name} ${healthLevelLabel(node.level)}`}
      data-testid="health-wall-cell"
      data-level={node.level}
      className={cn(
        'flex min-w-0 flex-col justify-between gap-1 rounded-md p-2 text-xs transition-colors',
        CELL_CLASS[node.level],
      )}
    >
      <span className="truncate font-medium text-foreground">{node.name}</span>
      <span className="flex items-center justify-between gap-1 font-mono text-muted-foreground">
        <span>{formatPct(node.cpuPct)}</span>
        <span>{node.running}/{node.crashed}</span>
      </span>
    </Link>
  )
}

/** 分级图例：各级计数与配色。 */
function HealthLegend({ nodes }: { nodes: HealthWallNode[] }) {
  const summary = summarizeHealthWall(nodes)
  const entries: { level: HealthLevel; count: number }[] = [
    { level: 'offline', count: summary.offline },
    { level: 'stale', count: summary.stale },
    { level: 'degraded', count: summary.degraded },
    { level: 'healthy', count: summary.healthy },
  ]
  return (
    <div data-testid="health-wall-legend" className="mt-3 flex flex-wrap items-center gap-3 border-t pt-3 text-xs text-muted-foreground">
      {entries.map(({ level, count }) => (
        <span key={level} className="inline-flex items-center gap-1.5">
          <span className={cn('inline-block size-2.5 rounded-sm', toneChipClass(healthLevelTone(level)))} aria-hidden="true" />
          {healthLevelLabel(level)} {count}
        </span>
      ))}
    </div>
  )
}

/**
 * 集群健康墙（FR-461）：逐节点热力矩阵 + 分级 + 排序 + 一键下钻。
 * 只读 CP 快照一次查询给全（零 Worker RPC）；行内排序由服务端 ?sort 完成。
 */
export function HealthWall({ enabled }: { enabled: boolean }) {
  const [sort, setSort] = useState<HealthWallSort>('level')
  const query = useHealthWall(enabled, sort)
  const nodes = query.data?.nodes ?? []

  let body
  if (query.isError) {
    body = <p className="py-8 text-center text-sm text-destructive">健康墙加载失败</p>
  } else if (nodes.length === 0) {
    body = <p className="py-8 text-center text-sm text-muted-foreground">暂无节点</p>
  } else {
    body = (
      <>
        <div data-testid="health-wall-grid" className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-6 xl:grid-cols-8">
          {nodes.map((node) => <HealthCell key={node.nodeId} node={node} />)}
        </div>
        <HealthLegend nodes={nodes} />
      </>
    )
  }

  return (
    <Panel
      data-testid="health-wall"
      title="集群健康墙"
      actions={
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          排序
          <select
            value={sort}
            onChange={(event) => setSort(event.target.value as HealthWallSort)}
            data-testid="health-wall-sort"
            className="rounded border bg-background px-2 py-1 text-xs"
          >
            {HEALTH_WALL_SORTS.map((key) => (
              <option key={key} value={key}>{healthWallSortLabel(key)}</option>
            ))}
          </select>
        </label>
      }
      bodyClassName="p-3"
    >
      {body}
    </Panel>
  )
}
