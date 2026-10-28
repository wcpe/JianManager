// 视图已回迁应用侧（原 ADR-102 迁包已撤销）；本层只做取数、两处受控状态（时间窗 / 归因 Tooltip）与两处接线（健康墙、链接）。
// 「需要处理」三路取数已上移至页眉消息中心，本层不再取异常实例 / 任务 / 告警。
import { useDeferredValue, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useNodes } from '@/api/nodes'
import { useMetricOverview, usePlatformObservabilityOverview, useResourceAttribution, usePlayerTrend, useSLO } from '@/api/metrics'
import { HealthWall } from '@/components/nodes/HealthWall'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import type { MetricRange } from '@jianmanager/ui'
import { OverviewPageView } from '@/components/views/overview/OverviewPageView'
import type { AttributionGauge } from '@/components/views/overview/OverviewPageView'
import { saveHomeMode, homeModeHref } from '@/lib/shared/home-mode'

/**
 * 概览页容器（ADR-102 a 范式）：六个查询、平台观测区权限判定、两处受控状态与三处接线都在这里决定，
 * 仪表盘、三张聚合卡、三张平台卡、趋势图与密集实例表交共享视图。
 *
 * 受控状态归属：`range` 与 `activeGauge` 归容器——前者是全部指标查询的查询键，后者既决定
 * 是否发起归因请求（`enabled`）又决定该请求的排序口径（`activeGauge === 'memory' ? 'memory' : 'cpu'`），
 * 两者都属取数口径；实例表的虚拟窗口、Tooltip 的键盘交互则是纯 UI 状态，留包内视图。
 * 保留同路径默认导出，路由表（`ROUTE_CHUNKS['/']`）与既有用例无需改动。
 */
export default function OverviewPage() {
  const [range, setRange] = useState<MetricRange>('24h')
  const navigate = useNavigate()
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
  const { data: overview } = useMetricOverview(range)
  /*
    【首页不再自带实例列表】原先这里还跑着 `useInfiniteInstanceSearch`（不限量无限滚动）
    并在页底铺全量实例表——它与「服务器」页的完整版本重复，且是本页渲染量的大头
    （约 783 个元素、单次主线程阻塞约 360ms）。移除后整页元素数与阻塞都随之下降；
    要看实例去「服务器」页，那里有搜索、筛选、分组与批量操作。
  */
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
  /*
    【首页补两个区块】
    - 玩家在线趋势：游戏服首页最该回答「现在多少人在玩、高峰在哪」，而这条数据早就有了，
      首页却一直没有。
    - 玩家 Top5：哪几台服在承载玩家，便于快速定位热点与做容量决策。

    两者都走现成接口（/metrics/players/trend、/metrics/instances/ranking），不新造契约。
    趋势自带 peakValue / dailyAvg / hourlyDist，无需前端二次聚合。
  */
  const playerTrend = usePlayerTrend({ range, tz: Intl.DateTimeFormat().resolvedOptions().timeZone })
  /*
    可用性 SLO（FR-463）：不单独占卡，并入趋势卡头部一行。
    它属于「长期健康」指标而非日常盯盘指标，但顺手看到又有价值（用户选择塞进现有卡片）。
    目标经 target 参数下发；口径与监控页同一接口，避免两处各算一套。
  */
  const slo = useSLO({ scope: 'platform', range })
  // 玩家 Top 服务器（第二行右侧）：与「服务器」页的实例列表不同，这里只回答「哪几台服在承载玩家」。

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
      canSeePlatformObs={canSeePlatformObs}
      platformOverview={platformObservability.data}
      platformOverviewLoading={platformObservability.isLoading}
      platformOverviewError={platformObservability.isError}
      // 健康墙本体是应用侧接线层（自持取数 + 排序状态），故按块注入；权限门禁与视图同一开关。
      renderHealthWall={() => <HealthWall enabled={canSeePlatformObs} />}
      playerTrend={playerTrend.data}
      playerTrendLoading={playerTrend.isLoading}
      playerTrendError={playerTrend.isError}
      slo={slo.data}
      // 包内不认识 react-router：下钻链接（异常项 / 任务项 / 归因 Tooltip / 「查看全部」）统一经插槽渲染。
      renderLink={({ href, className, testId, children }) => (
        <Link to={href} className={className} data-testid={testId}>
          {children}
        </Link>
      )}
      // 切回 AI 对话首页（FR-520 §4.4）：**先写记忆再导航**。
      // 顺序不能反——先导航会让新页面按旧记忆再判一次模式，出现一次闪回；
      // 且必须显式去掉 `home` 参数，否则经典的 `?home=classic` 会压过新写入的记忆。
      onSwitchToAssistant={() => {
        saveHomeMode('assistant')
        // 保留现有查询参数（FR-520 §8）：`home` 由 homeModeHref 删掉，
        // 其余（range 等）原样带走，用户切回经典时筛选条件还在。
        navigate(homeModeHref(null, window.location.search))
      }}
    />
  )
}
