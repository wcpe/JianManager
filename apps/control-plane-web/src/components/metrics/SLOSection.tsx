/* eslint-disable react-refresh/only-export-components -- 与组件同文件导出格式化纯函数，供单测直接断言（仓库既有约定） */
import { useTranslation } from 'react-i18next'
import { Percent } from 'lucide-react'
import { useSLO, type SLOResult } from '@/api/metrics'
import { Panel } from '@jianmanager/ui/components/panel'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import type { MetricRange } from '@jianmanager/ui'

/** 秒 → 紧凑时长（s/m/h/d）。null 表示无故障/无恢复，显式渲染「无故障」而非 0。 */
export function fmtDuration(seconds: number | null): string | null {
  if (seconds == null || !Number.isFinite(seconds)) return null
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) return `${(seconds / 60).toFixed(1)}m`
  if (seconds < 86400) return `${(seconds / 3600).toFixed(1)}h`
  return `${(seconds / 86400).toFixed(1)}d`
}

/** 可用率（0~1）→ 百分比；无分母时返回 '--'。 */
export function fmtAvailability(r: SLOResult | undefined): string {
  if (!r || r.totalSamples <= 0) return '--'
  return `${(r.availability * 100).toFixed(2)}%`
}

/**
 * 可用率进度条配色（可用率「越高越好」，与 `resourceLevel` 的「占用率越高越差」语义相反）。
 *
 * **必须显式传 level**：不传时 `MiniBar` 会调 `resourceLevel(pct)`，而该函数 `pct > 80 → 'danger'`
 * ——99.7% 的可用率会被染成满格**危险红**（语义完全反向），且在可用率真的偏低时同样是红色，
 * 等于不携带任何信息（自审 M2）。阈值取 SLO 目标：达标=success，未达标=danger。
 */
export function availabilityLevel(availability: number, target: number): 'success' | 'danger' {
  if (!Number.isFinite(availability) || !Number.isFinite(target)) return 'danger'
  return availability >= target ? 'success' : 'danger'
}

/** 误差预算消耗比（0~1+），无允许时长时返回 null；不适用（applicable=false）时同样返回 null。 */
export function budgetBurnRatio(r: SLOResult | undefined): number | null {
  if (!r || r.budgetAllowedSec <= 0) return null
  return r.budgetBurnedSec / r.budgetAllowedSec
}

/** 可用性区块的 props。 */
interface SLOSectionProps {
  /** 统计窗口（同时决定是否走降采样桶近似）。 */
  range: MetricRange
  /** 统计维度；缺省平台级汇总。 */
  scope?: 'platform' | 'node' | 'instance'
  /** node/instance 维度的目标 UUID（节点 UUID 或实例 UUID）；平台维度必须缺省。 */
  targetId?: string
}

/**
 * 可用性区块（FR-463）：窗口内可用率 / 故障次数 / MTTR / MTBF / 误差预算。
 * 口径：以「有可用证据的探针拍」为在线，缺拍计不可用（保守默认）；超 48h 窗口按降采样桶近似并标注。
 * MTTR/MTBF 为 null 时显式渲染「无故障」，不显示 0 也不显示 Infinity。
 * `applicable=false`（窗口内无任何可用证据）时整块显示「不适用」，
 * 不渲染「误差预算 100% 已消耗」这类误导性数字（m1）。
 */
export function SLOSection({ range, scope = 'platform', targetId }: SLOSectionProps) {
  const { t } = useTranslation()
  const { data, isError, isLoading } = useSLO({ scope, targetId, range })

  if (isError) {
    return (
      <Panel title={t('slo.title')} icon={<Percent className="size-4" />}>
        <p className="py-4 text-center text-sm text-muted-foreground">{t('slo.error')}</p>
      </Panel>
    )
  }

  if (isLoading) {
    return (
      <Panel title={t('slo.title')} icon={<Percent className="size-4" />}>
        <p className="py-4 text-center text-sm text-muted-foreground">{t('slo.loading')}</p>
      </Panel>
    )
  }

  const mttr = fmtDuration(data?.mttrSeconds ?? null)
  const mtbf = fmtDuration(data?.mtbfSeconds ?? null)
  const burn = budgetBurnRatio(data)

  return (
    <Panel
      title={
        <span className="flex items-center gap-2">
          {t('slo.title')}
          {data?.approximatedBuckets && (
            <span className="text-[11px] font-normal text-muted-foreground">{t('slo.approx')}</span>
          )}
        </span>
      }
      icon={<Percent className="size-4" />}
    >
      {data && !data.applicable ? (
        <p className="py-4 text-center text-sm text-muted-foreground">{t('slo.notApplicable')}</p>
      ) : !data ? (
        // 走到这里 = 响应缺失且非错误/加载态，唯一实际路径是**查询被禁用**
        // （`useSLO` 的 `enabled: enabled && (scope === 'platform' || !!targetId)`——
        // node/instance 维度漏传 targetId 时查询不下发，data 恒 undefined）。
        // 注意：不再为「applicable=true 却 totalSamples<=0」保留分支——真后端保证该态不存在
        // （instance/node 维有 `if total <= 0 { total = 1 }`，见 slo.go:82-84；platform 维分母为 0
        // 时直接走 `Applicable=false`，见 slo.go:213-217），而那一态已被上一分支拦下（自审 m3）。
        <p className="py-4 text-center text-sm text-muted-foreground">{t('slo.empty')}</p>
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
          <StatCard
            label={t('slo.availability')}
            value={fmtAvailability(data)}
            sub={t('slo.target') + ': ' + (data.target * 100).toFixed(1) + '%'}
            // 显式传 level：可用率「越高越好」，不能走 resourceLevel 的「占用率越高越差」阈值（M2）。
            bar={{ value: data.availability * 100, level: availabilityLevel(data.availability, data.target) }}
            title={`${t('slo.upSamples')}: ${data.upSamples} / ${t('slo.totalSamples')}: ${data.totalSamples}`}
          />
          <StatCard
            label={t('slo.incidents')}
            value={String(data.incidents)}
            sub={`${t('slo.activeIncidents')}: ${data.activeIncidents}`}
            tone={data.incidents > 0 ? 'warning' : 'neutral'}
          />
          <StatCard label={t('slo.mttr')} value={mttr ?? t('slo.noIncidents')} />
          <StatCard label={t('slo.mtbf')} value={mtbf ?? t('slo.noIncidents')} />
          <StatCard
            label={t('slo.budgetBurned')}
            value={burn == null ? t('slo.noIncidents') : `${(burn * 100).toFixed(1)}%`}
            tone={burn != null && burn >= 1 ? 'danger' : 'neutral'}
            title={`${t('slo.budgetAllowed')}: ${fmtDuration(data.budgetAllowedSec) ?? '--'}`}
          />
          {burn != null && <MiniBar value={Math.min(100, burn * 100)} />}
        </div>
      )}
    </Panel>
  )
}
