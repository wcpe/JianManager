import { useTranslation } from 'react-i18next'
import { Activity, Hash, Layers, Timer } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Tooltip, TooltipContent, TooltipTrigger } from '@jianmanager/ui/components/tooltip'
import type { FederationStreamItem } from '@/api/logFederation'
import { parseLogSource, type SourceNameLookup } from '@/lib/logs/logs-source-label'
import { levelColorClass, type LevelCount } from './insight-card-tokens'

/**
 * 日志洞察卡片（表格之外顶部）：事件总量、级别分布、主要来源、时间分布。
 *
 * # 为何从表格内部抬到外面
 *
 * 原先这些数字混在日志面板工具栏那一行里，与筛选控件争宽，既读不懂也点不到。
 * 抬到表格之外单独成卡片后：① 各占一块，标签能写人话；② 表格区只负责显示日志，
 * 不再被「当前视图 930 个事件（非精确总量）· 覆盖不完整 · 质量：EXACT / PARTIAL」
 * 这类一句话堆砌的徒标干扰；③ 滚动时洞察栏不跟着跑。
 *
 * # 为何不用 recharts
 *
 * 这里要的是堆叠条 / 横向条形 / 迷你柱阵——三者都是「宽高 = 比例」的简单矩形，
 * 手写几十行就能精控颜色 token 与无障碍语义。引一张图表引擎代价不成比例。
 */

/** 严重度序：堆叠条与图例按固定顺序排，不随后端返回顺序跳。 */
const LEVEL_ORDER = ['error', 'fatal', 'warn', 'warning', 'info', 'debug']
function levelRank(level: string): number {
  const index = LEVEL_ORDER.indexOf(level.toLowerCase())
  return index === -1 ? LEVEL_ORDER.length : index
}

/** 卡片外壳：统一标题、图标与右侧补充说明，避免三块各写一套间距。 */
export function InsightCard({
  icon,
  title,
  hint,
  children,
  testId,
  className,
}: {
  icon: React.ReactNode
  title: string
  /** 右侧小字说明（如“本页”这类限定）。 */
  hint?: React.ReactNode
  children: React.ReactNode
  testId: string
  className?: string
}) {
  return (
    <div
      data-testid={testId}
      className={cn(
        'flex h-full min-w-0 flex-col gap-1.5 overflow-hidden rounded-md border bg-card/50 px-2.5 py-2',
        className,
      )}
    >
      <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
        {icon}
        <span className="truncate font-medium">{title}</span>
        {hint ? <span className="ml-auto shrink-0 opacity-70">{hint}</span> : null}
      </div>
      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto">{children}</div>
    </div>
  )
}

/**
 * 级别分布：横向堆叠条 + 图例。
 *
 * # 为何用堆叠条而不是饼图
 *
 * 日志级别常年是「info 绝大多数 + 少量 warn/error」的偏态分布。
 * 饼图在 2/3 与 1/3 这种比例上很难目测比大小；堆叠条长度可直接比较，
 * 且能塞进卡片一行高度。Kibana / Splunk 也都是横向堆叠。
 */
export function LevelDistributionCard({
  levels,
  uncertain,
  failed,
}: {
  levels: LevelCount[]
  /** 计数可能不精确（覆盖不完整 / 维度被截断）：加 ≈ 前缀。 */
  uncertain?: boolean
  /**
   * 聚合查询本身失败。
   *
   * 失败时必须**仍然渲染卡片**：直接 return null 会让用户以为“没有级别数据”，
   * 而实际上是“没查到”。两种情形在运维上的下一步动作完全不同。
   */
  failed?: boolean
}) {
  const { t } = useTranslation()
  if (failed) {
    return (
      <InsightCard
        icon={<Layers className="size-3 shrink-0" />}
        title={t('logs.vizLevelTitle')}
        testId="logs-level-card"
      >
        <p data-testid="logs-level-failed" data-state="failed" className="text-[11px] text-status-warning">
          {t('logs.vizAggregateFailed')}
        </p>
      </InsightCard>
    )
  }
  // 固定严重度序：后端不保证顺序，而颜色块逐次跳动就没法扫视对比。
  const ordered = levels
    .filter((item) => item.count > 0)
    .slice()
    .sort((a, b) => levelRank(a.level) - levelRank(b.level))
  const sum = ordered.reduce((n, item) => n + item.count, 0)
  if (!ordered.length || sum <= 0) {
    return (
      <InsightCard icon={<Layers className="size-3 shrink-0" />} title={t('logs.vizLevelTitle')} testId="logs-level-card">
        <p data-testid="logs-level-empty" className="flex h-8 items-center text-[11px] text-muted-foreground">
          {t('logs.aggregateUnknown', { kind: t('logs.vizLevelTitle') })}
        </p>
      </InsightCard>
    )
  }

  return (
    <InsightCard
      icon={<Layers className="size-3 shrink-0" />}
      title={t('logs.vizLevelTitle')}
      testId="logs-level-card"
      hint={uncertain ? t('logs.vizApprox') : undefined}
    >
      <div
        className="flex h-3.5 w-full overflow-hidden rounded-sm bg-muted"
        role="img"
        aria-label={t('logs.vizLevelAria', {
          detail: ordered.map((item) => `${item.level} ${item.count}`).join(', '),
        })}
        // 具体数值落到 DOM：图形对读屏不可读，而 aria-label 在未加载 i18n
        // 资源的测试环境会退化成 key。这里是未经翻译的事实数据。
        data-level-detail={ordered.map((item) => `${item.level}:${item.count}`).join(',')}
        data-testid="logs-level-distribution"
      >
        {ordered.map((item) => (
          <div
            key={item.level}
            className={cn('h-full', levelColorClass(item.level))}
            style={{ width: `${(item.count / sum) * 100}%` }}
            title={`${item.level} ${item.count}`}
          />
        ))}
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[11px]">
        {ordered.map((item) => (
          <span
            key={item.level}
            data-testid="logs-level-legend"
            data-level={item.level}
            data-count={item.count}
            className="inline-flex items-center gap-1 text-muted-foreground"
          >
            <span aria-hidden className={cn('size-1.5 rounded-full', levelColorClass(item.level))} />
            <span className="uppercase">{item.level}</span>
            <span className="tabular-nums text-foreground">
              {uncertain ? '≈' : ''}{item.count.toLocaleString()}
            </span>
            <span className="text-muted-foreground">{Math.round((item.count / sum) * 100)}%</span>
          </span>
        ))}
      </div>
    </InsightCard>
  )
}

/**
 * 主要来源：横向条形，按条数降序。
 *
 * # 显示具体名称而不是「实例/节点」
 *
 * `inst:2/file` 这类原始标识直接给人看没人看得懂；而只写「实例」则完全丢失
 * “到底是哪个”。两者都不行，中间的正确形态是实例名（`logtest-mc`）。
 * 名字反查不到时退回 `实例 #2`：id 已知就说 id，而不是退回泛称。
 */
export function TopSourcesCard({
  items,
  lookup,
  countsReliable,
  failed,
}: {
  items: FederationStreamItem[]
  lookup: SourceNameLookup
  /** 上游是否保证计数可当断言（VL 截断时 hits 会被归零）。 */
  countsReliable?: boolean
  failed?: boolean
}) {
  const { t } = useTranslation()
  if (failed || !items.length) {
    return (
      <InsightCard icon={<Activity className="size-3 shrink-0" />} title={t('logs.vizSourceTitle')} testId="logs-source-card">
        <p data-testid="logs-source-empty" data-state={failed ? 'failed' : 'empty'} className={cn('flex h-8 items-center text-[11px]', failed ? 'text-status-warning' : 'text-muted-foreground')}>
          {failed ? t('logs.vizAggregateFailed', { kind: t('logs.vizSourceTitle') }) : t('logs.aggregateUnknown', { kind: t('logs.vizSourceTitle') })}
        </p>
      </InsightCard>
    )
  }
  // 后端不保证降序；不排序时「Top」可能是随机的，条形长度也就失去意义。
  const top = items.slice().sort((a, b) => b.hits - a.hits).slice(0, 5)
  const peak = Math.max(...top.map((item) => item.hits), 1)

  return (
    <InsightCard
      icon={<Activity className="size-3 shrink-0" />}
      title={t('logs.vizSourceTitle')}
      testId="logs-source-card"
      hint={!countsReliable ? t('logs.vizCountsUnreliable') : undefined}
    >
      <ul className="flex flex-col gap-1">
        {top.map((item) => {
          const parsed = parseLogSource(item.log_source_id)
          const resolved = parsed.id === null
            ? null
            : (parsed.kind === 'instance' ? lookup.instances : lookup.nodes)
              ?.find((entry) => entry.id === parsed.id)?.name ?? null
          // 名称缺失时的坦率写法：带类别与 id，而不是泛称。
          const name = resolved
            ?? (parsed.id !== null
              ? `${t(`logs.source_${parsed.kind}`, parsed.kind)} #${parsed.id}`
              : item.log_source_id || t('logs.streamsUnknownSource'))
          return (
            <li
              key={`${item.log_source_id}\u0000${item.stream}`}
              data-testid="logs-source-bar"
              data-source-id={item.log_source_id}
              data-source-name={name}
              className="flex items-center gap-2"
            >
              <span className="w-24 shrink-0 truncate text-[11px]" title={item.log_source_id}>
                {name}
              </span>
              <span className="h-2 min-w-0 flex-1 overflow-hidden rounded-sm bg-muted">
                <span
                  className="block h-full rounded-sm bg-status-info/70"
                  style={{ width: `${(item.hits / peak) * 100}%` }}
                />
              </span>
              <span className="w-12 shrink-0 text-right text-[11px] tabular-nums text-muted-foreground">
                {countsReliable ? item.hits.toLocaleString() : '?'}
              </span>
            </li>
          )
        })}
      </ul>
      <div className="mt-1 flex items-center justify-between text-[10px] text-muted-foreground" data-testid="logs-source-summary">
        <span>{t('logs.vizSourceCount', { count: items.length })}</span>
        <span className="tabular-nums">{countsReliable ? t('logs.vizSourceHits', { count: items.reduce((sum, item) => sum + item.hits, 0).toLocaleString() }) : t('logs.vizCountsUnreliable')}</span>
      </div>
    </InsightCard>
  )
}

export interface TimeDistributionBucket {
  label: string
  count: number
  /** At least one interval is unknown; count is only an observed subtotal. */
  missing?: boolean
}

/**
 * 时间分布：来自 Stats timeBucket 的全窗口柱阵。
 *
 * 当前页时间戳只代表已加载的 100 行，会把分页/预算截断误画成全量趋势。
 * 未返回的桶只有在聚合精确时才能算 0，其余缺口始终显示为未知。
 */
export function TimeDistributionCard({
  buckets,
  uncertain,
  failed,
  invalid,
}: {
  buckets: TimeDistributionBucket[]
  uncertain?: boolean
  failed?: boolean
  invalid?: boolean
}) {
  const { t, i18n } = useTranslation()
  const unsupported = invalid || buckets.length > 256
    || buckets.some((bucket) => !Number.isFinite(bucket.count) || bucket.count < 0 || !bucket.label)
  const hasMissing = buckets.some((bucket) => bucket.missing)
  const empty = !buckets.length || buckets.every((bucket) => !bucket.missing && bucket.count === 0)
  const state = failed ? 'failed' : unsupported ? 'unsupported' : empty ? 'empty' : hasMissing ? 'unknown' : 'ready'
  const unknown = t('logs.aggregateUnverified')
  const stateMessage = state === 'failed'
    ? t('logs.aggregateFailed', { kind: t('logs.vizTimeTitle') })
    : state === 'unsupported'
      ? t('logs.vizTimeUnsupported', {
        defaultValue: i18n?.resolvedLanguage?.startsWith('zh')
          ? '时间分布不可用：时间桶无效或不受支持'
          : 'Time distribution unavailable: invalid or unsupported time buckets',
      })
      : uncertain ? unknown : t('logs.empty')
  const detail = unsupported || failed ? '' : buckets.map((bucket) => `${bucket.label}:${bucket.missing ? '?' : bucket.count}`).join(',')
  const bucketTotal = buckets.reduce((sum, bucket) => sum + (bucket.missing ? 0 : bucket.count), 0)
  const peak = unsupported || failed ? 1 : Math.max(...buckets.filter((bucket) => !bucket.missing).map((bucket) => bucket.count), 1)

  return (
    <InsightCard
      icon={<Timer className="size-3 shrink-0" />}
      title={t('logs.vizTimeTitle')}
      testId="logs-time-card"
      hint={failed || unsupported ? undefined : uncertain || hasMissing ? unknown : t('logs.vizTimeWindow')}
    >
      {state === 'failed' || state === 'unsupported' || state === 'empty' ? (
        <div
          data-testid="logs-time-distribution"
          data-time-detail={detail}
          data-state={state}
          className="flex h-8 items-center text-[11px] text-muted-foreground"
          role="status"
        >
          <span className={cn('line-clamp-2', (state === 'failed' || state === 'unsupported') && 'text-status-warning')}>
            {stateMessage}
          </span>
        </div>
      ) : (
        <>
          <Tooltip>
            <TooltipTrigger asChild>
              <div
                data-testid="logs-time-distribution"
                data-time-detail={detail}
                data-state={state}
                className="flex h-8 items-end gap-px"
                role="img"
                aria-label={hasMissing ? `${t('logs.vizTimeAria')} · ${unknown}` : t('logs.vizTimeAria')}
              >
                {buckets.map((bucket) => (
                  <div
                    key={bucket.label}
                    data-missing={bucket.missing ? 'true' : undefined}
                    className={cn(
                      'min-w-0 flex-1 rounded-t-[1px]',
                      bucket.missing
                        ? 'flex items-center justify-center overflow-hidden border border-dashed border-status-warning/60 bg-status-warning/10 text-[9px] text-status-warning'
                        : bucket.count === 0 ? 'bg-muted' : 'bg-status-info/70',
                    )}
                    title={`${bucket.label}: ${bucket.missing ? `? · ${unknown}` : `${uncertain ? '≈' : ''}${bucket.count.toLocaleString()}`}`}
                    style={{ height: bucket.missing ? '100%' : `${Math.max(bucket.count === 0 ? 1 : 6, (bucket.count / peak) * 100)}%` }}
                  >
                    {bucket.missing ? <span aria-hidden>?</span> : null}
                  </div>
                ))}
              </div>
            </TooltipTrigger>
            <TooltipContent>
              {t('logs.vizTimeRangeHelp', {
                from: buckets[0]?.label ?? '',
                to: buckets[buckets.length - 1]?.label ?? '',
              })}
              {hasMissing ? ` · ? ${unknown}` : null}
            </TooltipContent>
          </Tooltip>
          <div data-testid="logs-time-summary" className="mt-1 flex items-center justify-between gap-2 text-[10px] text-muted-foreground">
            <span>{t('logs.vizBucketCount', { count: buckets.length })}</span>
            <span className="tabular-nums">{t('logs.vizBucketTotal', { count: bucketTotal })}</span>
            <span className="tabular-nums">{t('logs.vizBucketPeak', { count: peak })}</span>
          </div>
        </>
      )}
    </InsightCard>
  )
}

/**
 * 事件总量卡片。
 *
 * # 为何不直接把后端那串徒标扔到界面上
 *
 * 原来那行是「本次观测到 930 个事件（非精确总量）· 覆盖不完整 · 质量：EXACT / PARTIAL」，
 * 一句话里塞了四个不相关的度量，而且 `EXACT` / `PARTIAL` 是后端枚举值原样外泄。
 * 拆开后：主数字只表达「多少条」，其余状态用一句人话跟在下面，需要细节的看右上角徽标。
 *
 * # 为何计数不可信时不直接隐藏数字
 *
 * 「930」即使不是精确总量也是一个有意义的量级，比空着有用。所以显示数字，
 * 但前面加 `≈`，并在副标题里说清“不完全”。
 */
export function EventsTotalCard({
  count,
  exact,
  partial,
  pending,
  truncated,
  failed,
  quality,
  testId = 'logs-events-total',
  summary,
  pageRows,
  levelCount,
  streamCount,
  bucketCount,
  queryMeta,
}: {
  /** 事件数；undefined 表示尚未拿到。 */
  count?: number
  /** 后端保证计数精确。 */
  exact?: boolean
  /** 覆盖不完整（部分目标未命中）。 */
  partial?: boolean
  /** 覆盖尚未校验。 */
  pending?: boolean
  /** 结果被截断。 */
  truncated?: boolean
  /** 聚合查询失败。 */
  failed?: boolean
  /** 质量等级（EXACT / PARTIAL）：仅作 tooltip 细节，不直接当正文。 */
  quality?: string
  /** 测试用 id 前缀。 */
  testId?: string
  /**
   * 第二条副说明：例如「维度分布：查询失败，数量未知」。
   *
   * 统计与维度是两次独立请求，任一失败或不确定都需要让用户看到，
   * 而不是只报一个。
   */
  summary?: React.ReactNode
  /** Current page and aggregate metadata are shown together so the insight row is self-contained. */
  pageRows?: number
  levelCount?: number
  streamCount?: number
  bucketCount?: number
  queryMeta?: { elapsed_ms: number; returned_rows: number; limit: number; max_bytes: number; timeout_ms: number }
}) {
  const { t } = useTranslation()

  // 副标题只说一句最要紧的实话，不把四项状态堆成一行。
  const state = failed ? 'failed' : truncated ? 'truncated' : partial ? 'partial' : pending ? 'pending' : exact ? 'exact' : 'unverified'
  const note = {
    failed: t('logs.vizTotalFailed'),
    truncated: t('logs.vizTotalTruncated'),
    partial: t('logs.vizTotalPartial'),
    pending: t('logs.vizTotalPending'),
    exact: t('logs.vizTotalExact'),
    unverified: t('logs.vizTotalUnverified'),
  }[state]

  return (
    <InsightCard
      icon={<Hash className="size-3 shrink-0" />}
      title={t('logs.vizTotalTitle')}
      testId={testId}
      hint={quality && !exact ? <span title={t('logs.vizQualityValue', { quality })}>{quality}</span> : undefined}
    >
      <div className="flex items-baseline gap-1.5">
        <span
          className={cn(
            'text-xl font-semibold tabular-nums leading-none',
            failed && 'text-muted-foreground',
          )}
          data-testid={`${testId}-count`}
        >
          {failed || count === undefined ? '—' : `${exact ? '' : '≈'}${count.toLocaleString()}`}
        </span>
        <span className="text-[11px] text-muted-foreground">{t('logs.vizTotalUnit')}</span>
      </div>
      <p
        className={cn(
          'mt-0.5 text-[11px] leading-snug',
          failed ? 'text-status-warning' : 'text-muted-foreground',
        )}
        data-testid={`${testId}-note`}
        data-state={state}
      >
        {note}
      </p>
      {summary ? (
        <p
          className="mt-0.5 text-[11px] leading-snug text-muted-foreground"
          data-testid={`${testId}-summary`}
        >
          {summary}
        </p>
      ) : null}
      <div data-testid="logs-metric-summary" className="mt-2 grid grid-cols-4 gap-1 border-t border-border/60 pt-1.5 text-[10px]">
        {[
          [t('logs.vizPageRows'), pageRows],
          [t('logs.vizLevelCount'), levelCount],
          [t('logs.vizStreamCount'), streamCount],
          [t('logs.vizBucketCount'), bucketCount],
        ].map(([label, value]) => (
          <div key={String(label)} className="min-w-0">
            <div className="truncate text-muted-foreground">{label}</div>
            <div className="tabular-nums font-medium text-foreground">{value === undefined ? '—' : Number(value).toLocaleString()}</div>
          </div>
        ))}
      </div>
      {queryMeta ? (
        <p data-testid="logs-query-budget-summary" className="mt-1 truncate text-[10px] text-muted-foreground" title={t('logs.queryMeta', { ms: Math.round(queryMeta.elapsed_ms), rows: queryMeta.returned_rows, limit: queryMeta.limit, bytes: Math.round(queryMeta.max_bytes / (1024 * 1024)), seconds: queryMeta.timeout_ms / 1000 })}>
          {t('logs.queryMeta', { ms: Math.round(queryMeta.elapsed_ms), rows: queryMeta.returned_rows, limit: queryMeta.limit, bytes: Math.round(queryMeta.max_bytes / (1024 * 1024)), seconds: queryMeta.timeout_ms / 1000 })}
        </p>
      ) : null}
    </InsightCard>
  )
}
