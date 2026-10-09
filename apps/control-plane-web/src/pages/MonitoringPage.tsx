import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useNodes } from '@/api/nodes'
import { useInstanceSearch } from '@/api/instances'
import type { MetricRange, MetricResolution } from '@jianmanager/ui'
import {
  useBotRuntimeMetrics,
  useManagedProcessAction,
  useManagedProcessDetail,
  useProcessTop,
  type ManagedProcessAction,
  type ProcessTopItem,
} from '@/api/metrics'
import {
  INSTANCE_CHART_DEFS,
  NODE_CHART_DEFS,
  PLATFORM_CHART_DEFS,
  type MetricChartDef,
} from '@jianmanager/ui/lib/monitor-metrics'
import { useTargetSeries } from '@/components/charts/use-target-series'
import { DrillTargetPicker, type DrillTarget } from '@/components/charts/DrillTargetPicker'
import { InstanceRankingPanel } from '@/components/metrics/InstanceRankingPanel'
import { CapacityForecastCard } from '@/components/metrics/CapacityForecastCard'
import { SLOSection } from '@/components/metrics/SLOSection'
import MonitoringPageView from '@/components/views/instances/MonitoringPage'
import { useTranslation } from 'react-i18next'

/** 据 target 选用的图定义集（平台 4 / 节点 6 / 实例 6）。 */
function defsFor(kind: DrillTarget['kind']): MetricChartDef[] {
  if (kind === 'node') return NODE_CHART_DEFS
  if (kind === 'instance') return INSTANCE_CHART_DEFS
  return PLATFORM_CHART_DEFS
}

function defaultCompareMetric(kind: DrillTarget['kind']): string {
  if (kind === 'instance') return 'inst_tps'
  return 'node_cpu_pct'
}

function targetFromSearch(searchParams: URLSearchParams): DrillTarget {
  const instance = searchParams.get('instance')
  if (instance) return { kind: 'instance', uuid: instance }
  const node = searchParams.get('node')
  if (node) return { kind: 'node', uuid: node }
  return { kind: 'platform' }
}

/**
 * 统一监控页的应用接线层（ADR-097 a 范式）。
 *
 * 页面本体已迁入组件库并受控；本层持有下钻目标/范围/粒度/对比选中/进程选择等状态、
 * 接七处取数（节点、实例精确查、进程 TOP、进程详情、处置动作、Bot 运行时、序列），
 * 并把序列 hook 与图表定义集注入视图（主图网格要它们才画得出来）。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function MonitoringPage() {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const { data: nodes } = useNodes()
  const [target, setTarget] = useState<DrillTarget>(() => targetFromSearch(searchParams))
  /**
   * 把 URL 里的实例 uuid 解析成数字 id（`useProcessTop` 要 id）。
   *
   * 用 `/instances/search?uuid=` 精确查一条，而不是拉全量实例再本地 find——后者在千级规模下
   * 约 1MB/轮且带 30 秒兜底轮询，而这里每次只需要一个实例。
   */
  const resolvedInstanceUuid = target.kind === 'instance' ? target.uuid : ''
  const { data: instancePage } = useInstanceSearch(
    { uuid: resolvedInstanceUuid, page: 1, pageSize: 1 },
    resolvedInstanceUuid !== '',
  )
  // 页级范围 + 粒度（驱动概览/对比；主图网格各图另有独立范围，但共享该页级粒度）。
  const [range, setRange] = useState<MetricRange>('24h')
  const [resolution, setResolution] = useState<MetricResolution>('auto')
  // 多指标对比默认选中一个关键指标；随 target 切换重置。
  const [compareSel, setCompareSel] = useState<string[]>(() => [defaultCompareMetric(targetFromSearch(searchParams).kind)])
  const [selectedProcess, setSelectedProcess] = useState<ProcessTopItem | null>(null)
  const [pendingAction, setPendingAction] = useState<ManagedProcessAction | null>(null)

  const currentInstance = instancePage?.items[0]
  const currentNode = target.kind === 'node' ? (nodes ?? []).find((node) => node.uuid === target.uuid) : undefined
  const currentNodeUUID = currentNode?.uuid
  const { data: processTop = [] } = useProcessTop({
    instanceId: currentInstance?.id,
    nodeId: currentNodeUUID,
    enabled: target.kind !== 'instance' || !!currentInstance,
  })
  const selectedInstanceId = selectedProcess?.instanceId
  const selectedPid = selectedProcess?.pid
  const processDetail = useManagedProcessDetail(selectedInstanceId, selectedPid, !!selectedProcess)
  const processAction = useManagedProcessAction()
  const botRuntime = useBotRuntimeMetrics({
    nodeId: currentNode?.id,
    range,
    resolution,
    enabled: target.kind === 'node' && !!currentNode,
  })
  const botRuntimeReason = botRuntime.data?.unavailable.find((item) => item.nodeId === currentNode?.id)?.reason

  // 概览/对比/主图共享的数据源描述（MonitorSource 与 SeriesTarget 同构）。
  const source = target.kind === 'platform' ? { kind: 'platform' as const } : { kind: target.kind, uuid: target.uuid }
  // 概览/对比共享的原始序列（页级范围 + 粒度）。
  const { series: raw, isLoading } = useTargetSeries(source, range, resolution)

  // 当前实例的世界名列表（来自其分世界序列），供下钻到世界的下拉。
  const worlds = useMemo(() => {
    if (target.kind !== 'instance') return []
    const set = new Set<string>()
    for (const s of raw) if (s.world) set.add(s.world)
    return [...set].sort()
  }, [raw, target.kind])

  const toggleCompare = (metricKey: string) =>
    setCompareSel((prev) =>
      prev.includes(metricKey) ? prev.filter((k) => k !== metricKey) : [...prev, metricKey],
    )

  // target 切换时重置对比选择（不同 target 指标目录不同）。
  const onChangeTarget = (next: DrillTarget) => {
    if (next.kind !== target.kind) setCompareSel([defaultCompareMetric(next.kind)])
    setTarget(next)
  }

  const actionInstanceName = processDetail.data?.instance.name || selectedProcess?.instanceUuid || '--'
  const actionScope = pendingAction === 'kill_tree' ? '目标 PID 及其子进程树' : '仅目标 PID'
  const actionMode = pendingAction === 'kill_tree' ? '强制终止树' : '温和终止'
  const actionDescription = selectedProcess
    ? `实例 ${actionInstanceName}，PID ${selectedProcess.pid}，模式 ${actionMode}，影响范围：${actionScope}。后端会再次确认 PID 归属并要求 confirm=true。`
    : t('monitor.processActionDescription', '将只作用于该实例当前受管进程树内的目标 PID；后端会再次确认 PID 归属并要求 confirm=true。')

  return (
    <MonitoringPageView
      target={target}
      range={range}
      onRangeChange={setRange}
      resolution={resolution}
      onResolutionChange={setResolution}
      compareSel={compareSel}
      onCompareToggle={toggleCompare}
      raw={raw}
      rawLoading={isLoading}
      processTop={processTop}
      processDetail={processDetail.data}
      processDetailError={processDetail.isError}
      processDetailRefreshing={processDetail.isFetching}
      processActionPending={processAction.isPending}
      botRuntimeReason={botRuntimeReason}
      selectedProcess={selectedProcess}
      onSelectedProcessChange={setSelectedProcess}
      pendingAction={pendingAction}
      onPendingActionChange={setPendingAction}
      onProcessAction={async (payload) => {
        await processAction.mutateAsync(payload)
      }}
      onRefreshProcessDetail={() => void processDetail.refetch()}
      useSeries={useTargetSeries}
      actionDescription={actionDescription}
      chartDefs={defsFor(target.kind)}
      rankingSlot={<InstanceRankingPanel nodes={nodes} />}
      forecastSlot={
        target.kind !== 'platform' ? (
          <CapacityForecastCard scope={target.kind} targetId={target.uuid} range={range} />
        ) : null
      }
      sloSlot={
        <SLOSection
          range={range}
          scope={target.kind === 'platform' ? 'platform' : target.kind}
          targetId={target.kind === 'platform' ? undefined : target.uuid}
        />
      }
      drillSlot={
        <DrillTargetPicker
          target={target}
          onChange={onChangeTarget}
          nodes={nodes ?? []}
          worlds={worlds}
        />
      }
    />
  )
}
