import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Loader2, Sparkles } from 'lucide-react'
import { usePerformanceAttribution, type AttributionResult, type AttributionFactor } from '@/api/metrics'
import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { MiniBar } from '@jianmanager/ui/components/mini-bar'
import type { MetricRange } from '@jianmanager/ui'

/**
 * 因子指标键 → i18n 显示名键。
 *
 * 覆盖后端 `attributionCandidates`（attribution.go:192-200）的**全部 7 类候选**：
 * 后端 `Label` 是直出的中文字符串（`"GC 暂停占用"`/`"已加载区块"`/…），只映射 2 条会让
 * 英文界面把其余因子的中文原样漏出（自审 M5）。
 * `inst_gc_*` 两条沿用既有键（`MetricsSegment` 的 GC **曲线**标题已改走 `metrics.gc*`，
 * 这里的因子显示名是它们唯一且正确的用途）；其余 5 条为本次补齐。
 */
const FACTOR_LABEL_KEY: Record<string, string> = {
  inst_gc_time_ms: 'attribution.gcTime',
  inst_gc_count: 'attribution.gcCount',
  world_loaded_chunks: 'attribution.factorLoadedChunks',
  world_entities: 'attribution.factorEntities',
  world_tile_entities: 'attribution.factorTileEntities',
  heap_ratio: 'attribution.factorHeapRatio',
  inst_threads: 'attribution.factorThreads',
}

/** 因子显示名：优先本地化 key，否则用后端 label（中文口径），再否则用 metricKey。 */
function factorLabel(f: AttributionFactor, t: TFunction): string {
  const key = FACTOR_LABEL_KEY[f.metricKey]
  if (key) return t(key)
  return f.label || f.metricKey
}

/**
 * 结论句（`tldr`）本地化。
 *
 * 后端 `attributionTLDR`（attribution.go:310）返回 `"劣化主因：%s（权重 %.2f）"` 的**中文字面量**，
 * 直接渲染会让英文界面整卡漏中文。前端据 `factors[0]` 自行拼装，后端 `tldr` 仅作兜底
 * （因子名本地化后与后端串不一致，故非「重复渲染」而是「按同一语义重新渲染」）。
 */
function tldrText(result: AttributionResult, t: TFunction): string {
  const top = result.factors[0]
  if (!top) return result.tldr
  return t('attribution.tldr', { factor: factorLabel(top, t), weight: top.weight.toFixed(2) })
}

/** 相关性 → 带符号三位小数（负=因子随 TPS 下降而上升，即疑似主因方向）。 */
function fmtCorr(r: number): string {
  if (!Number.isFinite(r)) return '--'
  return `${r >= 0 ? '+' : ''}${r.toFixed(3)}`
}

/** 正相关 = 该因子随目标指标一同上升，不解释「劣化」→ 中性呈现（避免误导成主因）。 */
function factorLevel(correlation: number): 'danger' | 'neutral' {
  return correlation <= 0 ? 'danger' : 'neutral'
}

/** 归因结果视图 props。 */
interface AttributionResultViewProps {
  /** 后端归因结果（`status=insufficient` 时只显示原因与样本数）。 */
  result: AttributionResult
}

/** 后端直出中文的 note（`attribution.go:278` 恒为「相关性非因果」）→ 本地化文案；未知 note 原样保留。 */
function noteText(note: string | undefined, t: TFunction): string {
  if (!note) return ''
  // 仅替换已知的那一条后端固定文案，其余（含将来新增）原样透出，避免把未知内容吞掉。
  return note === '相关性非因果' ? t('attribution.noteCorrelation') : note
}

/**
 * 归因结果视图（FR-465）：一句话结论 + 因子权重条。insufficient 时只显示原因，不给排序。
 */
export function AttributionResultView({ result }: AttributionResultViewProps) {
  const { t } = useTranslation()
  if (result.status !== 'ok' || result.factors.length === 0) {
    return (
      <div className="space-y-1">
        <p className="text-sm text-muted-foreground">{t('attribution.insufficient')}</p>
        <p className="text-[11px] text-muted-foreground">
          {t('attribution.samples')}: {result.samples}
        </p>
      </div>
    )
  }
  // note 可选（DTO 与 contracts 均为可选字段）：为空时不得留下尾随分隔符（自审 m5）。
  const note = noteText(result.factors[0]?.note, t)
  return (
    <div className="space-y-3">
      <p className="text-sm font-medium">{tldrText(result, t)}</p>
      <ul className="space-y-2.5">
        {result.factors.map((f) => (
          <li key={f.metricKey} className="space-y-1">
            <div className="flex items-baseline justify-between gap-2 text-sm">
              <span className="min-w-0 truncate font-medium">{factorLabel(f, t)}</span>
              <span className="shrink-0 tabular-nums text-muted-foreground">
                {(f.weight * 100).toFixed(1)}%
                <span className="ml-2 text-[11px]">
                  {t('attribution.correlation')} {fmtCorr(f.correlation)}
                </span>
              </span>
            </div>
            <div className="flex items-center gap-2">
              <MiniBar value={f.weight * 100} level={factorLevel(f.correlation)} />
              <span className="shrink-0 text-[11px] text-muted-foreground">
                {factorLevel(f.correlation) === 'danger' ? t('attribution.weight') : t('attribution.neutral')}
              </span>
            </div>
          </li>
        ))}
      </ul>
      <p className="text-[11px] text-muted-foreground">
        {t('attribution.samples')}: {result.samples}
        {note ? ` · ${note}` : ''}
      </p>
    </div>
  )
}

/** 性能归因卡 props。 */
interface AttributionCardProps {
  /** 目标实例 UUID（归因按实例维度算）。 */
  instanceUuid: string
  /** 统计窗口；随窗口滑动重算。 */
  range: MetricRange
}

/**
 * 性能归因卡片（FR-465）：**按需触发**（点击「分析」），不做常驻轮询——
 * 归因是窗口级相关分析，随每拍变动无意义且会放大查询负载。
 */
export function AttributionCard({ instanceUuid, range }: AttributionCardProps) {
  const { t } = useTranslation()
  const [requested, setRequested] = useState(false)
  const { data, isFetching, isError, refetch } = usePerformanceAttribution({
    targetId: instanceUuid,
    range,
    enabled: requested,
  })

  return (
    <Panel
      title={t('attribution.title')}
      icon={<Sparkles className="size-4" />}
      actions={
        <Button
          type="button"
          variant="outline"
          size="xs"
          disabled={isFetching}
          // 已取过数时走 `refetch()`：`setRequested(true)` 在 `requested` 已为 true 时是同值更新，
          // React 不会重渲染、`enabled`/`queryKey` 均不变，TanStack Query 也不重新取数——
          // 结果是「重新分析」点了毫无反应（自审 M3）。
          onClick={() => (requested ? void refetch() : setRequested(true))}
        >
          {isFetching && <Loader2 className="size-3.5 animate-spin" />}
          {data ? t('attribution.reanalyze') : t('attribution.analyze')}
        </Button>
      }
    >
      <p className="mb-3 text-[11px] text-muted-foreground">{t('attribution.hint')}</p>
      {isError ? (
        <p className="text-sm text-muted-foreground">{t('attribution.error')}</p>
      ) : isFetching ? (
        <p className="text-sm text-muted-foreground">{t('attribution.loading')}</p>
      ) : data ? (
        <AttributionResultView result={data} />
      ) : (
        <p className="text-sm text-muted-foreground">{t('common.noData')}</p>
      )}
    </Panel>
  )
}
