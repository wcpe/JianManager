// 视图已回迁应用侧（原 ADR-097 迁包已撤销）；本层只做六个查询取数、两处受控状态（时间窗 / 归因 Tooltip）
// 与三处接线（健康墙、链接、按需续取守卫）。
import { useCallback, useDeferredValue, useState } from 'react'
import { Link } from 'react-router'
import { useNodes } from '@/api/nodes'
import { useInfiniteInstanceSearch, useInstanceSearch } from '@/api/instances'
import { useTasks } from '@/api/tasks'
import { useAlertEvents } from '@/api/alerts'
import { useMetricOverview, usePlatformObservabilityOverview, useResourceAttribution } from '@/api/metrics'
import { HealthWall } from '@/components/HealthWall'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import type { MetricRange } from '@jianmanager/ui'
import { OverviewPageView } from '@/components/views/overview/OverviewPageView'
import type { AttributionGauge } from '@/components/views/overview/OverviewPageView'

/**
 * 概览页容器（ADR-097 a 范式）：六个查询、平台观测区权限判定、两处受控状态与三处接线都在这里决定，
 * 仪表盘、三张聚合卡、三张平台卡、趋势图与密集实例表交共享视图。
 *
 * 受控状态归属：`range` 与 `activeGauge` 归容器——前者是全部指标查询的查询键，后者既决定
 * 是否发起归因请求（`enabled`）又决定该请求的排序口径（`activeGauge === 'memory' ? 'memory' : 'cpu'`），
 * 两者都属取数口径；实例表的虚拟窗口、Tooltip 的键盘交互则是纯 UI 状态，留包内视图。
 * 保留同路径默认导出，路由表（`ROUTE_CHUNKS['/']`）与既有用例无需改动。
 */
export default function OverviewPage() {
  const [range, setRange] = useState<MetricRange>('24h')
  // 已展开的归因 Tooltip；null=全部收起（此时归因查询不发起）。
  const [activeGauge, setActiveGauge] = useState<AttributionGauge | null>(null)
  const isPlatformAdmin = useAuthStore((state) => state.role === 10)
  // FR-432：平台观测区与权限树对齐（node.read / monitor.read 或超管）
  const hasPerm = usePermissionsStore((s) => s.hasPerm)
  const canSeePlatformObs = isPlatformAdmin || hasPerm('monitor.read') || hasPerm('node.read')
  const { data: nodes } = useNodes()
  /**
   * 实例数据分两路取，各自只拿需要的量：
   *
   * - 异常卡片只要 CRASHED 的前 5 条，交给服务端按状态过滤——原先是先拉全集再本地筛，
   *   千级规模下等于为一张 5 行的卡片付约 1MB 的代价
   * - 底部密集表保留「可看全部」的能力，但改为滚动按需分页：首屏只取一页，滚到底再取下一页，
   *   不再一次性拉全量（`useInfiniteInstanceSearch` 就是为 1000+ 实例的滚动浏览设计的）
   */
  const exceptionQuery = useInstanceSearch({
    status: 'CRASHED',
    page: 1,
    pageSize: 5,
    sort: 'name',
    order: 'asc',
  })
  const instancesQuery = useInfiniteInstanceSearch({ sort: 'name', order: 'asc' })
  const tasksQuery = useTasks({ limit: 5 })
  const alertsQuery = useAlertEvents({ resolved: false, pageSize: 5 })
  const { data: overview } = useMetricOverview(range)
  // 【为什么用 useDeferredValue】本页渲染约 783 个元素，而 `useMetricOverview` 每 10 秒轮询一次
  // （见 api/metrics.ts），每次更新都会让整页重渲染——实测单次主线程阻塞约 360ms，
  // 且 30 秒观测里稳定复现 4 次（间隔精确 10s）。用户点击侧栏时与之叠加，阻塞可达 900ms，
  // 体感就是「点什么都卡」。
  // 把数据更新降级为可中断的低优先级渲染后：本次（用旧值）渲染先同步走完、结果相同因而
  // 几乎不产生工作；真正带新值的那次渲染由 React 在空闲时切片执行，用户操作可以插队。
  // 实时性不变（仍是 10 秒粒度），只是不再抢主线程。
  //
  // 降级留在容器（而非视图内再降级一次）：轮询来自这里，旧值/新值的分发点也就该在这里。
  const deferredOverview = useDeferredValue(overview)
  const attribution = useResourceAttribution(activeGauge !== null, activeGauge === 'memory' ? 'memory' : 'cpu')
  const platformObservability = usePlatformObservabilityOverview(canSeePlatformObs)
  const instanceRows = instancesQuery.data?.pages.flatMap((p) => p.items) ?? []
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = instancesQuery

  /**
   * 滚动到近底时续取下一页。
   *
   * 守卫（有没有下一页 / 是否正在取）留在容器：视图只上报「用户滚到底了」，不持查询状态，
   * 也就不会把分页判定漏进包内的虚拟滚动逻辑（与 InstancesPage 的 `loadMoreInstances` 同范式）。
   */
  const loadMoreInstances = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) {
      void fetchNextPage()
    }
  }, [fetchNextPage, hasNextPage, isFetchingNextPage])

  return (
    <OverviewPageView
      range={range}
      onRangeChange={setRange}
      overview={deferredOverview}
      nodeTotal={nodes?.length}
      activeGauge={activeGauge}
      onActiveGaugeChange={setActiveGauge}
      attribution={attribution.data}
      attributionLoading={attribution.isLoading}
      attributionError={attribution.isError}
      exceptionInstances={exceptionQuery.data?.items}
      exceptionLoading={exceptionQuery.isLoading}
      exceptionError={exceptionQuery.isError}
      recentTasks={tasksQuery.data?.items}
      recentTasksLoading={tasksQuery.isLoading}
      recentTasksError={tasksQuery.isError}
      activeAlerts={alertsQuery.data?.items}
      activeAlertsLoading={alertsQuery.isLoading}
      activeAlertsError={alertsQuery.isError}
      canSeePlatformObs={canSeePlatformObs}
      platformOverview={platformObservability.data}
      platformOverviewLoading={platformObservability.isLoading}
      platformOverviewError={platformObservability.isError}
      // 健康墙本体是应用侧接线层（自持取数 + 排序状态），故按块注入；权限门禁与视图同一开关。
      renderHealthWall={() => <HealthWall enabled={canSeePlatformObs} />}
      instances={instanceRows}
      onNeedMoreInstances={loadMoreInstances}
      // 包内不认识 react-router：下钻链接（异常项 / 任务项 / 归因 Tooltip / 「查看全部」）统一经插槽渲染。
      renderLink={({ href, className, testId, children }) => (
        <Link to={href} className={className} data-testid={testId}>
          {children}
        </Link>
      )}
    />
  )
}
