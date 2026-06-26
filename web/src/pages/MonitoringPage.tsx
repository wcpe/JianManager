import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { Panel } from '@/components/ui/panel'
import { MonitorSkeleton, type MonitorSource } from '@/components/charts/MonitorSkeleton'
import {
  NODE_CHART_DEFS,
  INSTANCE_CHART_DEFS,
  PLATFORM_CHART_DEFS,
  type MetricChartDef,
} from '@/lib/monitor-metrics'

/** 选中的监控对象：平台聚合 / 某节点 / 某实例。 */
type Target =
  | { kind: 'platform' }
  | { kind: 'node'; uuid: string }
  | { kind: 'instance'; uuid: string }

/** 据 target 选用的图定义集（平台 4 / 节点 6 / 实例 6）。 */
function defsFor(kind: Target['kind']): MetricChartDef[] {
  if (kind === 'node') return NODE_CHART_DEFS
  if (kind === 'instance') return INSTANCE_CHART_DEFS
  return PLATFORM_CHART_DEFS
}

/**
 * 统一监控页（FR-169，design §4.2）：平台/节点/实例三 target 可切，套同一监控骨架——
 * 每图独立时间筛选 + 底部 brush 拖拽轴 + hover 浮窗 + 实时（轮询）。
 * 仅消费现有 FR-060/061 时序指标，不改后端。进程 TOP10 不在本页（已拆后端 backlog）。
 */
export default function MonitoringPage() {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()
  const [target, setTarget] = useState<Target>({ kind: 'platform' })

  const targetValue = useMemo(
    () => (target.kind === 'platform' ? 'platform' : `${target.kind}:${target.uuid}`),
    [target],
  )

  const onSelect = (value: string) => {
    if (value === 'platform') {
      setTarget({ kind: 'platform' })
      return
    }
    const [kind, uuid] = value.split(':')
    if (kind === 'node') setTarget({ kind: 'node', uuid })
    else if (kind === 'instance') setTarget({ kind: 'instance', uuid })
  }

  // target → 数据源描述（驱动骨架取数）。key 含 target 标识，切换时整树重挂，重置各图 brush/range。
  const source: MonitorSource =
    target.kind === 'platform' ? { kind: 'platform' } : { kind: target.kind, uuid: target.uuid }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-bold">{t('monitor.title')}</h1>
        <div className="flex items-center gap-2">
          <label className="text-xs text-muted-foreground" htmlFor="monitor-target">
            {t('monitor.target')}
          </label>
          <select
            id="monitor-target"
            value={targetValue}
            onChange={(e) => onSelect(e.target.value)}
            className="h-8 rounded-md border bg-background px-2 text-sm"
          >
            <option value="platform">{t('monitor.platform')}</option>
            {(nodes?.length ?? 0) > 0 && (
              <optgroup label={t('monitor.nodes')}>
                {nodes?.map((n) => (
                  <option key={n.uuid} value={`node:${n.uuid}`}>
                    {n.name}
                  </option>
                ))}
              </optgroup>
            )}
            {(instances?.length ?? 0) > 0 && (
              <optgroup label={t('monitor.instances')}>
                {instances?.map((i) => (
                  <option key={i.uuid} value={`instance:${i.uuid}`}>
                    {i.name}
                  </option>
                ))}
              </optgroup>
            )}
          </select>
        </div>
      </div>

      <Panel bodyClassName="px-3 py-2 text-[11px] text-muted-foreground">{t('monitor.hint')}</Panel>

      <MonitorSkeleton key={targetValue} defs={defsFor(target.kind)} source={source} />
    </div>
  )
}
