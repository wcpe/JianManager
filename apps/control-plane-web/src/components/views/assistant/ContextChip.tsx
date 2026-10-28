import { Link } from 'react-router'
import { ArrowUpRight, X } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import api from '@/api/client'
import type { InstanceMetricsData } from '@/lib/instances/instance-metrics'
import type { PageContextEntityRef } from '@/lib/assistant/page-context'

/**
 * 芯片摘要用的指标查询（**不轮询**）。
 *
 * 为什么不复用 `useInstanceMetrics`：它带 `refetchInterval: 10s`，那是给实例详情页
 * 「实时」语义用的。聊天框里每枚芯片都每 10 秒拉一次，等于把监控页的轮询复制进聊天，
 * 而用户可能只是把服务器拖进来问一句话。这里取一次即可，需要最新值时移除重加。
 *
 * queryKey 与 `useInstanceMetrics` **故意不同**：共用 key 会因 `refetchInterval` 选项
 * 不同而互相干扰（React Query 以首个注册者的选项为准，后来者静默失效）。
 */
function useChipMetrics(instanceId: number, enabled: boolean) {
  return useQuery({
    queryKey: ['assistantChipMetrics', instanceId],
    queryFn: async () => {
      const { data } = await api.get<InstanceMetricsData>(`/instances/${instanceId}/metrics`)
      return data
    },
    enabled: !!instanceId && enabled,
    refetchInterval: false,
    staleTime: 30_000,
    retry: false,
  })
}

/**
 * 一枚上下文芯片（FR-518 §5.2）。
 *
 * 实例类芯片额外带**指标摘要**与**跳转入口**，因为「把它拖进来」之后的下一句
 * 几乎总是「它为什么卡 / 它现在什么状态」——让用户再自己跑一趟实例页是多余的。
 *
 * 三条实现约束：
 *  1. **指标按需取**：只有展开（或指针悬停）才发请求。千级列表里每枚芯片都自动拉指标，
 *     等于把实例页的轮询复制到聊天框里。
 *  2. **指标失败不打扰**：拿不到就显示「指标不可用」，不弹错、不影响输入与发送。
 *     指标是辅助信息，不该把它变成阻塞项（与「工具失败要如实报」是两件事：
 *     前者是用户没主动要求的数据，后者是用户要求的动作结果）。
 *  3. **跳转用 `<Link>` 而非裸 `<a>`**：保住 SPA 路由与既有导航历史，
 *     且与侧边栏一致的激活语义。
 *
 * 可访问性：移除与跳转都是**独立可聚焦按钮**，`aria-label` 落在按钮本身
 * （护栏要求：可访问名不能只挂在外层容器上，否则交互触发不了）。
 */
export interface ContextChipProps {
  chipKey: string
  label: string
  onRemove: () => void
  entity?: PageContextEntityRef
  /** 指标请求开关（跟随折叠/浮窗可见性，避免隐藏时仍取数）。 */
  metricsEnabled?: boolean
}

/** 把 MiB 压成人类可读。指标接口的 `memoryMb` 单位就是 MiB，不要当字节再除 1024²。 */
function formatMb(mb: number): string {
  if (!Number.isFinite(mb) || mb <= 0) return '0M'
  if (mb < 1024) return `${Math.round(mb)}M`
  const gb = Math.round((mb / 1024) * 10) / 10
  return `${Number.isInteger(gb) ? gb : gb.toFixed(1)}G`
}

export function ContextChip({
  chipKey,
  label,
  onRemove,
  entity,
  metricsEnabled = true,
}: ContextChipProps) {
  const { t } = useTranslation()
  // id 允许 string|number（深链用 uuid、列表行给数字主键）。
  // 指标接口只认数字主键，故这里显式归一；归不出数字就当非实例处理，
  // 不拿 uuid 去拼指标 URL（那会得到一个必然 404 的请求）。
  const instanceId = entity?.type === 'instance' ? Number(entity.id) : 0
  const hasInstance = Number.isFinite(instanceId) && instanceId > 0
  const isInstance = hasInstance
  // 非实例芯片不取指标：`enabled=false` 让 hook 不发请求（不是「发了再忽略」）。
  const metricsQuery = useChipMetrics(hasInstance ? instanceId : 0, hasInstance && metricsEnabled)
  const metrics = metricsQuery.data

  // 摘要内容按接口的**显式可用性位**裁剪，不用 0 / '-' 冒充真值
  // （instance-metrics.ts 的约定：`*Available=false` 必须渲染成「不可用」）。
  let summary: string | null = null
  if (metricsQuery.isLoading) {
    summary = t('assistant.chipMetricsLoading', '指标…')
  } else if (metrics) {
    const parts: string[] = []
    if (metrics.probeAvailable) {
      parts.push(`${t('assistant.chipCpu', 'CPU')} ${Math.round(metrics.cpuPercent)}%`)
      parts.push(formatMb(metrics.memoryMb))
      if (Number.isFinite(metrics.tps)) {
        parts.push(`TPS ${Math.round(metrics.tps * 10) / 10}`)
      }
    } else if (metrics.slpAvailable || metrics.queryAvailable) {
      // 无探针但有 SLP/Query：能给的就只有版本与在线人数。
      if (metrics.versionAvailable && metrics.version) parts.push(metrics.version)
      if (metrics.playersAvailable) {
        parts.push(`${metrics.onlinePlayers}/${metrics.maxPlayers}`)
      }
    }
    // 一个字段都拿不到 → 不编造，交给下面的「指标不可用」。
    summary = parts.length > 0 ? parts.join(' · ') : null
  }
  if (summary === null) {
    summary = t('assistant.chipMetricsUnavailable', '指标不可用')
  }

  return (
    <span
      key={chipKey}
      className="inline-flex items-center gap-1 rounded-full border border-border/70 bg-muted/50 py-0.5 pl-2 pr-1 text-[11px]"
    >
      <span className="max-w-[12rem] truncate" title={label}>
        {label}
      </span>

      {isInstance ? (
        <>
          {/* 指标摘要：按可用性位裁剪；全都拿不到就明说「不可用」，不假装 0。 */}
          <span className="text-muted-foreground/80">{summary}</span>
          <Link
            to={`/instances/${instanceId}`}
            aria-label={t('assistant.chipOpenInstance', '打开「{{label}}」的实例详情', { label })}
            title={t('assistant.chipOpenInstance', '打开「{{label}}」的实例详情', { label })}
            className="rounded-full p-0.5 text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <ArrowUpRight className="size-3" />
          </Link>
        </>
      ) : null}

      <button
        type="button"
        aria-label={t('assistant.chipRemove', '移除「{{label}}」', { label })}
        title={t('assistant.chipRemove', '移除「{{label}}」', { label })}
        className="rounded-full p-0.5 hover:bg-accent"
        onClick={onRemove}
      >
        <X className="size-3" />
      </button>
    </span>
  )
}
