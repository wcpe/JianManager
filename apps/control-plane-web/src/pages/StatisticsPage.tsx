// 视图已回迁应用侧（迁包已撤销，见 ADR-103）；本层只做五个查询取数、统计窗口、管理员门禁与两个区块的接线。
import { useState } from 'react'
import { useNodes } from '@/api/nodes'
import { useInstanceAggregate } from '@/api/instances'
import { useOnlinePlayers } from '@/api/players'
import { useMetricOverview } from '@/api/metrics'
import { useClientDistObservability } from '@/api/clientStats'
import { useAuthStore } from '@/stores/auth'
import type { MetricRange } from '@jianmanager/ui'
import { SLOSection } from '@/components/metrics/SLOSection'
import { PlayerTrendCard } from '@/components/metrics/PlayerTrendCard'
import { StatisticsPageView } from '@/components/views/statistics/StatisticsPageView'

/** 平台管理员角色位（与后端 roles.ts 同源，应用侧权威副本；包内不持鉴权状态）。 */
const ROLE_PLATFORM_ADMIN = 10

/** 客户端分发观测端点的区间枚举（24h/7d/30d/90d/180d，ADR-049）。 */
type ObsRange = '24h' | '7d' | '30d' | '90d' | '180d'

/** 把页面统一的 MetricRange 映射到分发观测端点支持的区间枚举（小时档归 24h，180d 由 90d 升）。 */
function toObsRange(r: MetricRange): ObsRange {
  switch (r) {
    case '1h':
    case '6h':
    case '24h':
      return '24h'
    case '7d':
      return '7d'
    case '30d':
      return '30d'
    case '90d':
      return '90d'
    case '1y':
      return '180d'
    default:
      return '7d'
  }
}

/**
 * 观测·统计页容器（ADR-103 a 范式）：五个平台级查询、统计窗口与分发观测的权限门禁都在这里决定，
 * KPI、构成分布与分发区块交共享视图。
 *
 * 受控状态归属：`range` 归容器——它是全部查询的查询键，且分发观测端点只认 `toObsRange` 换算出的
 * 区间枚举（换算含策略，属取数口径）；平台管理员判定同样归容器，因为非管理员必须**不发起**该请求。
 * 可用性区块与玩家趋势卡各自带应用侧接线层（取数 hook + 浏览器时区读取），故以插槽注入。
 * 保留同路径默认导出，路由表（`ROUTE_CHUNKS['/statistics']`）与既有用例无需改动。
 */
export default function StatisticsPage() {
  const [range, setRange] = useState<MetricRange>('7d')

  const isPlatformAdmin = useAuthStore((s) => s.role) === ROLE_PLATFORM_ADMIN

  const overviewQuery = useMetricOverview(range)
  const nodesQuery = useNodes()
  /**
   * 实例维度走后端聚合（`/instances/aggregate`）：视图不再为「状态 / 角色 / 进程类型」三张分布图
   * 拉全量实例列表（千级约 1MB/轮，且带 30 秒兜底轮询、页面还持有全集）。
   */
  const instAggQuery = useInstanceAggregate()
  const playersQuery = useOnlinePlayers()
  // 分发观测仅平台管理员可查；非管理员不发起请求（enabled=false），UI 整区降级。
  const distQuery = useClientDistObservability({ range: toObsRange(range), enabled: isPlatformAdmin })

  /**
   * 平台规模/构成区的取数失败标记（详见视图的 `statsError` 文档）。
   *
   * 三个查询都是 KPI 与构成分布的唯一来源，任一失败都会把「取数失败」显示成实打实的 0
   * （如实例数 0/0、探针连通 0.0%、分布面板空白），必须显式降级。
   *
   * **`/metrics/overview` 刻意不纳入**：它的 totals 只是优先口径，失败后 KPI 会回退到
   * `/nodes`、`/instances/aggregate` 的本地聚合，呈现的仍是真实数字（既有用例 ④ 即锁定这一点），
   * 把它算进来只会让「有完整降级数据」的页面变成一片错误态。
   */
  const statsError = nodesQuery.isError || instAggQuery.isError || playersQuery.isError

  return (
    <StatisticsPageView
      range={range}
      onRangeChange={setRange}
      overviewTotals={overviewQuery.data?.totals}
      nodes={nodesQuery.data}
      instanceCounts={instAggQuery.data}
      players={playersQuery.data}
      isPlatformAdmin={isPlatformAdmin}
      statsError={statsError}
      distribution={distQuery.data}
      distributionError={distQuery.isError}
      // 两个区块的取数分别在应用侧接线层内（useSLO / usePlayerTrend + 时区读取），故只交出渲染结果。
      renderSloSection={() => <SLOSection range={range} scope="platform" />}
      renderPlayerTrend={() => <PlayerTrendCard range={range} />}
    />
  )
}
