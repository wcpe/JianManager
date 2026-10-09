// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做列表分页取数、筛选态、候选用户、导出与骨架/错误态接线。
import { useMemo, useState } from 'react'
import { useAuditLogs, exportAuditLogs } from '@/api/audit'
import { useUsers } from '@/api/users'
import { AuditPageView } from '@/components/views/audit/AuditPageView'
import { DEFAULT_AUDIT_FILTER, toAuditParams } from '@/lib/audit-filters'
import type { AuditFilterState } from '@/lib/audit-filters'

/**
 * 审计日志页容器（ADR-097 a 范式）：分页取数、筛选态、候选用户与导出都在这里决定，
 * 筛选条、时间线行与展开详情交共享视图。
 *
 * 受控状态归属：`filter` 归容器——它经 `toAuditParams` 变成 `GET /audit` 的查询键，
 * 任一项变化都触发重新取数，且无限分页按 queryKey 天然回到第 1 页；
 * 展开行目标与其实测高度等纯展示状态留在视图内。
 * 导出与列表共用同一份 `params`（不含分页参数），只有这里同时持有两者。
 * 保留同路径默认导出，路由表（`ROUTE_CHUNKS['/audit']`）与既有用例无需改动。
 */
export default function AuditPage() {
  const { data: users } = useUsers()

  const [filter, setFilter] = useState<AuditFilterState>(DEFAULT_AUDIT_FILTER)

  const params = toAuditParams(filter)
  const { data, isLoading, isError, fetchNextPage, hasNextPage, isFetchingNextPage } = useAuditLogs(params)
  const logs = useMemo(() => data?.pages.flatMap((page) => page.items) ?? [], [data])

  /**
   * 导出当前筛选命中的日志（NDJSON 白名单导出）。
   * Blob 下载是浏览器副作用，故留在容器：视图只上报点击，按钮禁用态仍按「已加载行为 0」判定。
   */
  const handleExport = async () => {
    if (logs.length === 0) return
    const blob = await exportAuditLogs(params)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `audit-${new Date().toISOString().replace(/[:.]/g, '-')}.ndjson`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <AuditPageView
      logs={logs}
      // 信封总数（含未加载行）；`undefined` 表示首屏尚未到达，视图据此渲染同壳骨架。
      total={data?.pages[0]?.total}
      isLoading={isLoading}
      isError={isError}
      users={users}
      filter={filter}
      hasNextPage={hasNextPage}
      isFetchingNextPage={isFetchingNextPage}
      onChangeFilter={(patch) => setFilter((prev) => ({ ...prev, ...patch }))}
      onResetFilter={() => setFilter(DEFAULT_AUDIT_FILTER)}
      onLoadMore={() => void fetchNextPage()}
      onExport={() => void handleExport()}
    />
  )
}
