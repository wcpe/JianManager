// 视图已迁至 @jianmanager/ui（ADR-097）；本层只注入健康墙快照与路由链接（受控化）。
import { useState } from 'react'
import { Link } from 'react-router'
import { HealthWall as HealthWallView } from '@/components/views/nodes/HealthWall'
import { useHealthWall, type HealthWallSort } from '@/api/metrics'

/**
 * 集群健康墙（FR-461）：逐节点热力矩阵 + 分级 + 排序 + 一键下钻。
 * 只读 CP 快照一次查询给全（零 Worker RPC）；行内排序由服务端 ?sort 完成。
 * 排序键留在本层——它决定取数。
 */
export function HealthWall({ enabled }: { enabled: boolean }) {
  const [sort, setSort] = useState<HealthWallSort>('level')
  const query = useHealthWall(enabled, sort)

  return (
    <HealthWallView
      nodes={query.data?.nodes ?? []}
      truncated={query.data?.truncated ?? false}
      isError={query.isError}
      sort={sort}
      onSortChange={setSort}
      renderLink={({ href, title, ariaLabel, className, level, children }) => (
        <Link to={href} title={title} aria-label={ariaLabel} className={className} data-testid="health-wall-cell" data-level={level}>
          {children}
        </Link>
      )}
    />
  )
}
