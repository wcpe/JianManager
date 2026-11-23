import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router'
import { usePageContext } from '@/lib/assistant/page-context'
import { useNodes } from '@/api/nodes'
import { useInstanceSearch } from '@/api/instances'
import type { MetricRange, MetricResolution } from '@jianmanager/ui'
import { useBotRuntimeMetrics, useManagedProcessAction, useManagedProcessDetail, useProcessTop } from '@/api/metrics'
import type { ManagedProcessAction, ProcessTopItem } from '@/api/metrics'
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

function entityKey(target: DrillTarget): string {
  return target.kind === 'platform' ? 'platform:' : `${target.kind}:${target.uuid}`
}

function targetFromSearch(searchParams: URLSearchParams): DrillTarget {
  const instance = searchParams.get('instance')
  if (instance) return { kind: 'instance', uuid: instance }
  const node = searchParams.get('node')
  if (node) return { kind: 'node', uuid: node }
  return { kind: 'platform' }
}

/** 
 * 统一监控页的应用接线层。
 *
 * 页面本体是受控视图（见 components/views）；本层持有下钻目标/范围/粒度/对比选中/进程选择等状态、
 * 接七处取数（节点、实例精确查、进程 TOP、进程详情、处置动作、Bot 运行时、序列），
 * 并把序列 hook 与图表定义集注入视图（主图网格要它们才画得出来）。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function MonitoringPage() {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const { data: nodes } = useNodes()
  const [target, setTarget] = useState<DrillTarget>(() => targetFromSearch(searchParams))
  // 页级范围 + 粒度（驱动概览/对比；主图网格各图另有独立范围，但共享该页级粒度）。
  const [range, setRange] = useState<MetricRange>('24h')
  const [resolution, setResolution] = useState<MetricResolution>('auto')
  const [compareSel, setCompareSel] = useState<string[]>(() => [defaultCompareMetric(targetFromSearch(searchParams).kind)])
  const [selectedProcess, setSelectedProcess] = useState<ProcessTopItem | null>(null)
  const [pendingAction, setPendingAction] = useState<ManagedProcessAction | null>(null)

  // URL 导航与选择器共享实体切换规则；同实例的 world/来源节点变化不改变进程归属。
  const changeTarget = (next: DrillTarget) => {
    if (entityKey(next) !== entityKey(target)) {
      if (next.kind !== target.kind) setCompareSel([defaultCompareMetric(next.kind)])
      setSelectedProcess(null)
      setPendingAction(null)
    }
    setTarget(next)
  }

  // URL → 状态：浏览器前进/后退、或从别处再点一条 `?instance=` 深链时，页面必须跟着走。
  //
  // 用 React 推荐的「渲染期根据 props 变化调整 state」而不是 useEffect：后者会在提交后
  // 再触发一轮渲染（lint 也会报 set-state-in-effect）。这里只在 URL 目标与当前目标不一致
  // 时重置一次，普通选择器切换（已同步写 URL）不会进入这个分支。
  const urlTargetKey = searchParams.get('instance')
    ? `instance:${searchParams.get('instance')}`
    : searchParams.get('node')
      ? `node:${searchParams.get('node')}`
      : 'platform:'
  const targetKey = entityKey(target)
  const [syncedUrlKey, setSyncedUrlKey] = useState(urlTargetKey)
  if (urlTargetKey !== syncedUrlKey) {
    setSyncedUrlKey(urlTargetKey)
    const next = targetFromSearch(searchParams)
    if (entityKey(next) !== targetKey) changeTarget(next)
  }

  // 页面上报（§4.2）：监控页发「下钻目标 + 时间窗」，不发原始时序点全量。
  // 目标来自 URL（`?instance=<uuid>` / `?node=<uuid>`），与页面自身解析结果同源。
  usePageContext(
    useMemo(
      () => ({
        pageKey: 'monitor',
        ...(target.kind === 'instance'
          ? { entities: [{ type: 'instance' as const, id: target.uuid }] }
          : target.kind === 'node'
            ? { entities: [{ type: 'node' as const, id: target.uuid }] }
            : {}),
      }),
      [target],
    ),
  )
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

  // 只认「确实解析到当前 target」的实例。useInstanceSearch 带 keepPreviousData，
  // 切到别的实例/平台/节点后旧结果仍会短暂留在 data 里；不看 target 就沿用它，
  // 进程榜会继续按旧实例 ID 查询（甚至把旧进程行展示在新目标下）。
  const currentInstance =
    target.kind === 'instance' && instancePage?.items[0]?.uuid === target.uuid ? instancePage.items[0] : undefined
  const currentNode = target.kind === 'node' ? (nodes ?? []).find((node) => node.uuid === target.uuid) : undefined
  const currentNodeUUID = currentNode?.uuid
  // 节点目标必须解析成功才查进程榜：nodes 未加载完/加载失败/uuid 不存在时，
  // 空 nodeId 会让管理员拿到全局进程榜（与页面目标不符）。
  const processTopEnabled = target.kind === 'instance' ? !!currentInstance : target.kind === 'node' ? !!currentNodeUUID : true
  const { data: cachedProcessTop = [], isError: cachedProcessTopError } = useProcessTop({
    instanceId: currentInstance?.id,
    nodeId: currentNodeUUID,
    enabled: processTopEnabled,
  })
  // disabled 查询仍会返回同 key 的平台缓存（包括错误）；目标未解析时不可交给视图或供选中。
  const processTop = processTopEnabled ? cachedProcessTop : []
  const processTopError = processTopEnabled && cachedProcessTopError

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
  const { series: raw, isLoading, isError: rawError } = useTargetSeries(source, range, resolution)

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

  // target 切换：同步重置对比选择、清空旧目标的进程选择，并把新目标写回 URL。
  //
  // 为什么要清 selectedProcess/pendingAction：目标已经变了，旧进程的详情弹窗/确认弹窗
  // 再留着就是对“当前目标”的错误陈述（尤其两份数据同源时用户很难分辨）。
  // 为什么要写 URL：深链/刷新/分享都以 URL 为准，只改 state 会让地址栏与实际视图长期不一致。
  const onChangeTarget = (next: DrillTarget) => {
    changeTarget(next)
    const params = new URLSearchParams(searchParams)
    params.delete('instance')
    params.delete('node')
    if (next.kind === 'instance') params.set('instance', next.uuid)
    if (next.kind === 'node') params.set('node', next.uuid)
    setSearchParams(params, { replace: true })
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
      rawError={rawError}
      processTop={processTop}
      processTopError={processTopError}
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
