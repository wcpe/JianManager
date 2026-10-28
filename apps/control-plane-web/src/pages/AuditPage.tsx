// 视图已回迁应用侧（迁包已撤销，见 ADR-103）；本层只做列表分页取数、筛选态、候选用户、导出与骨架/错误态接线。
import { useMemo, useState } from 'react'
import { useAuditLogs, exportAuditLogs } from '@/api/audit'
import { useUserSearch } from '@/api/users'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { AuditPageView } from '@/components/views/audit/AuditPageView'
import { DEFAULT_AUDIT_FILTER, toAuditParams } from '@/lib/audit/audit-filters'
import type { AuditFilterState } from '@/lib/audit/audit-filters'

/** 用户候选窗口大小：靠键入服务端 `q` 缩小，与 InstancePicker / 群组成员候选同款。 */
const AUDIT_USER_LIMIT = 50

/**
 * 审计日志页容器（ADR-103 a 范式）：分页取数、筛选态、候选用户与导出都在这里决定，
 * 筛选条、时间线行与展开详情交共享视图。
 *
 * 受控状态归属：`filter` 归容器——它经 `toAuditParams` 变成 `GET /audit` 的查询键，
 * 任一项变化都触发重新取数，且无限分页按 queryKey 天然回到第 1 页；
 * 展开行目标与其实测高度等纯展示状态留在视图内。
 * 导出与列表共用同一份 `params`（不含分页参数），只有这里同时持有两者。
 * 保留同路径默认导出，路由表（`ROUTE_CHUNKS['/audit']`）与既有用例无需改动。
 */
export default function AuditPage() {
  /**
   * 文本类筛选取 300ms 防抖：输入框必须即时回显（`filter` 原样喂给视图），
   * 但请求键只用停止键入后的值——否则每敲一个字都重取一页审计日志（还带无限分页的叠加页）。
   * 时间与用户下拉是离散选择，不需要防抖。
   */
  const [userKeyword, setUserKeyword] = useState('')
  const userQ = useDebounced(userKeyword, 300).trim()
  // 用户候选走服务端搜索（FR-336）：默认前 N 条 + 键入 `q`，不再拉全量用户。
  const { data: userPage } = useUserSearch({
    ...(userQ ? { q: userQ } : {}),
    limit: AUDIT_USER_LIMIT,
    offset: 0,
  })

  const [filter, setFilter] = useState<AuditFilterState>(DEFAULT_AUDIT_FILTER)
  const debouncedAction = useDebounced(filter.action, 300)
  const debouncedTargetType = useDebounced(filter.targetType, 300)

  const params = useMemo(
    () => toAuditParams({ ...filter, action: debouncedAction, targetType: debouncedTargetType }),
    [filter, debouncedAction, debouncedTargetType],
  )
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
    // 【为什么要挂进 DOM】未挂载的 <a> 在部分浏览器（Firefox / Safari 的历史实现）里
    // `click()` 不会真的触发下载导航；且紧接着 `revokeObjectURL` 会把这些浏览器里**尚未取到字节**
    // 的下载掐断。挂载后再点，并在移除后用 setTimeout 让出当前任务再撤销 URL，
    // 下载已开始（URL 已被引用）时撤销才是安全的。
    document.body.appendChild(a)
    a.click()
    a.remove()
    setTimeout(() => URL.revokeObjectURL(url), 0)
  }

  return (
    <AuditPageView
      logs={logs}
      // 信封总数（含未加载行）；`undefined` 表示首屏尚未到达，视图据此渲染同壳骨架。
      total={data?.pages[0]?.total}
      isLoading={isLoading}
      isError={isError}
      users={userPage?.items}
      usersTotal={userPage?.total}
      onUserQueryChange={setUserKeyword}
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
