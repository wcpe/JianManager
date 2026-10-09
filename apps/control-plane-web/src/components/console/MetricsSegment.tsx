import { useState } from 'react'
import { toast } from 'sonner'
import { useMetricSeries, useInstanceMetrics } from '@/api/metrics'
import { useInstance } from '@/api/instances'
import { useInstanceCapabilities } from '@/lib/capabilities'
import { useProbeUpdateStatus, useUpdateProbe } from '@/api/probe'
import {
  useInstanceProbeVersion,
  useSelectableProbeVersions,
  useSetInstanceProbeVersion,
} from '@/api/artifactVersions'
import type { MetricRange } from '@jianmanager/ui'
import { AttributionCard } from '@/components/metrics/AttributionCard'
import { CapacityForecastCard } from '@/components/metrics/CapacityForecastCard'
import MetricsSegmentView, {
  type ProbeUpdateCardData,
  type ResourceLimitData,
} from '@/components/views/instances/MetricsSegment'

/**
 * 实例监控段的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体已迁入组件库并受控；本层取历史序列与实时指标、判定探针卡是否适用、
 * 接探针更新/版本切换，并把两张各需取数的卡（归因 / 容量预测）以插槽交回。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function MetricsSegment({ instanceUuid, instanceId }: { instanceUuid: string; instanceId: number }) {
  const [range, setRange] = useState<MetricRange>('24h')
  const { data, isLoading } = useMetricSeries({ scope: 'instance', targetId: instanceUuid, range })
  const { data: inst } = useInstance(instanceId)
  const running = inst?.status === 'RUNNING'
  const { data: directMetrics } = useInstanceMetrics(instanceId, running)

  // 能力画像（FR-445，ADR-091）：探针卡只在画像声明「metrics 能力以 probe 为来源」时渲染。
  // ServerProbe 是 Bukkit 插件，代理/二进制/beacon 等非 MC 实例无法加载——改由画像 `sources`
  // 判定，取代原先写死的 `if (inst?.role === 'proxy') return null`（避免「更新探针必失败」的陷阱按钮）。
  const profile = useInstanceCapabilities(inst)
  const probeCapable = profile.sources?.metrics?.includes('probe') ?? false
  const { data: st } = useProbeUpdateStatus(instanceId)
  const update = useUpdateProbe(instanceId)
  const { data: selectable } = useSelectableProbeVersions()
  const { data: selection } = useInstanceProbeVersion(instanceId)
  const setVersion = useSetInstanceProbeVersion(instanceId)

  // ServerProbe 是 Bukkit 插件，只有 Minecraft Java 服务端能加载：代理端（BungeeCord/Waterfall/
  // Velocity）、Beacon 配套服务（role=beacon）与通用二进制（type=generic）都无法加载。
  // 与后端 model.IsProbeApplicable 同口径：这类实例不渲染探针卡，避免「更新探针必失败」的陷阱按钮。
  const probeApplicable = inst?.type === 'minecraft_java' && inst?.role !== 'proxy' && inst?.role !== 'beacon'

  let probeCard: ProbeUpdateCardData | null = null
  if (probeCapable && probeApplicable && st) {
    probeCard = {
      bridgeConnected: !!st.probeConnected,
      metricsActive: !!directMetrics?.probeAvailable,
      version: st.version || '',
      versionOrigin: st.versionOrigin,
      lastPushedAt: st.lastPushedAt ?? undefined,
      versionId: st.versionId ?? 0,
      versionError: st.versionError ?? undefined,
      selectableVersions: (selectable?.versions ?? []).map((v) => ({ id: v.id, version: v.version })),
      selectionVersionId: selection?.versionId ?? 0,
      resolvedVersion: selection?.resolvedVersion?.version || '',
      updating: update.isPending,
      changing: setVersion.isPending,
    }
  }

  // 资源限额卡只在 docker 实例且设了至少一项上限时渲染。
  const cpuLimit = inst?.cpuLimit ?? 0
  const memLimit = inst?.memLimitMb ?? 0
  const diskLimit = inst?.diskLimitMb ?? 0
  const hasAnyLimit = cpuLimit > 0 || memLimit > 0 || diskLimit > 0
  const memUsedMb =
    running && directMetrics && directMetrics.memoryMb > 0 ? directMetrics.memoryMb : null
  const resourceLimit: ResourceLimitData | null =
    inst?.processType === 'docker' && hasAnyLimit
      ? { cpuLimit, memLimitMb: memLimit, diskLimitMb: diskLimit, memUsedMb }
      : null

  return (
    <MetricsSegmentView
      instanceId={instanceId}
      range={range}
      onRangeChange={setRange}
      series={data?.series ?? []}
      loading={isLoading}
      metrics={directMetrics}
      running={!!running}
      probeCard={probeCard}
      onProbeUpdate={(restart) => update.mutateAsync(restart)}
      onProbeVersionChange={(versionId) => setVersion.mutateAsync(versionId).then(() => undefined)}
      resourceLimit={resourceLimit}
      attributionSlot={<AttributionCard instanceUuid={instanceUuid} range={range} />}
      forecastSlot={<CapacityForecastCard scope="instance" targetId={instanceUuid} range={range} />}
      notify={(kind, message) => {
        if (kind === 'success') toast.success(message)
        else toast.error(message)
      }}
    />
  )
}
