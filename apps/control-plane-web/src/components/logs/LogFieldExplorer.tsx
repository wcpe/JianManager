import { useId, useState } from 'react'
import { hashKey } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ListFilter, RefreshCw } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { NativeSelect, NativeSelectOption } from '@jianmanager/ui/components/native-select'
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from '@jianmanager/ui/components/table'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@jianmanager/ui/components/tabs'
import { Popover, PopoverContent, PopoverDescription, PopoverHeader, PopoverTitle, PopoverTrigger } from '@jianmanager/ui/components/popover'
import { useLogsFederationFacets, useLogsFederationStats } from '@/api/logFederation'
import {
  buildFieldFacetParams,
  buildFieldStatsParams,
  fieldExplorerStatus,
  fieldStatsValues,
  fieldValueExpression,
  isQueryableLogField,
  LOG_FIELD_TIME_BUCKETS,
} from './log-field-explorer-utils'
import { LOG_FIELD_EXPLORER_NS } from './log-field-explorer-locales'

export interface LogFieldExplorerProps {
  /** The caller's aggregate params, including the same fixed viewId and authorization scope. */
  params: Record<string, unknown>
  /** Only instance VL context may enable analysis; file-tail views must pass false. */
  enabled: boolean
  fields: readonly string[]
  /** Append this already quoted, parenthesized filter to the query. */
  onFilter: (expression: string) => void
  fieldsStatus?: 'loading' | 'ready' | 'failed' | 'unsupported'
  onRetryFields?: () => void
}

const selectClass = 'w-full min-w-0'
const ROW_LIMIT = 100

/** A fixed-size trigger; all exploration and scanning stay inside an on-demand popover. */
export function LogFieldExplorer(props: LogFieldExplorerProps) {
  const { t } = useTranslation(LOG_FIELD_EXPLORER_NS)
  const [open, setOpen] = useState(false)
  const fileSource = props.params.view === 'platform' || ['worker', 'control_plane'].includes(String(props.params.source ?? ''))
  const contextReady = props.enabled && !fileSource && typeof props.params.viewId === 'string' && props.params.viewId.trim().length > 0
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm" data-testid="log-field-explorer-trigger">
          <ListFilter className="size-3.5" aria-hidden />
          {t('title')}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-[min(760px,calc(100vw-24px))] max-h-[min(70vh,640px)] overflow-y-auto" aria-label={t('title')}>
        <PopoverHeader>
          <PopoverTitle>{t('title')}</PopoverTitle>
          <PopoverDescription>{t('intro')}</PopoverDescription>
        </PopoverHeader>
        {/* A scope/view change drops selections and old observers instead of analyzing a stale field. */}
        {open && <FieldExplorerPanel key={hashKey([props.params])} {...props} enabled={contextReady} />}
      </PopoverContent>
    </Popover>
  )
}

function FieldExplorerPanel({ params, enabled, fields, onFilter, fieldsStatus = 'ready', onRetryFields }: LogFieldExplorerProps) {
  const { t } = useTranslation(LOG_FIELD_EXPLORER_NS)
  const id = useId()
  const [field, setField] = useState('')
  const [mode, setMode] = useState<'values' | 'stats'>('values')
  const [groupBy, setGroupBy] = useState('')
  const [timeBucket, setTimeBucket] = useState('')
  const [analysis, setAnalysis] = useState<{ groupBy: string; timeBucket: string } | null>(null)
  const [formError, setFormError] = useState(false)
  const options = [...new Set(fields)].filter((name) => name.length > 0).sort()
  const validField = field.length > 0 && options.includes(field) && isQueryableLogField(field)
  const queryEnabled = enabled && fieldsStatus === 'ready' && validField
  // TanStack hashes these bounded params by value; manual memoization is unnecessary.
  const facetParams = validField ? buildFieldFacetParams(params, field) : { ...params }
  const statsParams = queryEnabled && analysis
    ? buildFieldStatsParams(params, field, analysis.groupBy, analysis.timeBucket)
    : { ...params }
  const facets = useLogsFederationFacets(facetParams, queryEnabled && mode === 'values')
  const stats = useLogsFederationStats(statsParams, queryEnabled && mode === 'stats' && analysis !== null)
  const activeQuery = mode === 'values' ? facets : stats
  const dimensions = Array.isArray(facets.data?.dimensions) ? facets.data.dimensions : []
  const dimension = dimensions.find((item) => item.dimension === field)
  const rawValues = Array.isArray(dimension?.values) ? dimension.values : []
  const values = rawValues.filter((item) => typeof item.value === 'string' && Number.isFinite(item.count) && item.count >= 0)
  const response = mode === 'values'
    ? facets.data && { ...facets.data, truncated: facets.data.truncated || dimension?.truncated || rawValues.length > 20 }
    : stats.data
  const status = fieldExplorerStatus(response, activeQuery.error)
  const invalidDimension = mode === 'values' && facets.isSuccess && (!dimension || !Array.isArray(dimension.values) || values.length !== rawValues.length) && !status.issues.includes('unsupported')
  const points = Array.isArray(stats.data?.points) ? stats.data.points : []
  const invalidStats = mode === 'stats' && stats.isSuccess && !Array.isArray(stats.data.points) && !status.issues.includes('unsupported')
  const numericMissing = mode === 'stats' && stats.isSuccess && points.length > 0 && points.some((point) => {
    const metrics = fieldStatsValues(point)
    return metrics.count === undefined || (metrics.count > 0 && (metrics.sum === undefined || metrics.min === undefined || metrics.max === undefined || metrics.avg === undefined))
  })
  const hasResponse = activeQuery.isSuccess || activeQuery.isError
  const hasRun = mode === 'values' || analysis !== null
  const uncertain = status.issues.length > 0
  const failed = activeQuery.isError || status.issues.includes('failed') || status.issues.includes('unsupported')
  const retry = () => {
    if (!queryEnabled || !hasRun || activeQuery.isFetching) return
    void activeQuery.refetch()
  }

  if (!enabled) return <p role="status" className="mt-3 text-xs text-muted-foreground">{t('contextRequired')}</p>
  if (fieldsStatus !== 'ready') {
    return (
      <div role={fieldsStatus === 'loading' ? 'status' : 'alert'} className="mt-3 space-y-2 text-xs">
        <p>{t(fieldsStatus === 'loading' ? 'fieldsLoading' : fieldsStatus === 'failed' ? 'fieldsFailed' : 'fieldsUnsupported')}</p>
        {onRetryFields && fieldsStatus !== 'loading' && <Button size="sm" variant="outline" onClick={onRetryFields}>{t('retry')}</Button>}
      </div>
    )
  }

  return (
    <div className="mt-3 space-y-3 text-xs">
      <label htmlFor={`${id}-field`} className="flex items-center gap-2">
        <span className="shrink-0 font-medium">{t('field')}</span>
        <NativeSelect size="sm" id={`${id}-field`} value={field} className={`${selectClass} flex-1`} onChange={(event) => {
          setField(event.target.value)
          setAnalysis(null)
          setFormError(false)
        }}>
          <NativeSelectOption value="">{t('chooseField')}</NativeSelectOption>
          {options.map((name) => <NativeSelectOption key={name} value={name}>{name}</NativeSelectOption>)}
        </NativeSelect>
      </label>
      {options.length === 0 && <p role="status">{t('noFields')}</p>}
      {field && !options.includes(field) && <p role="alert">{t('staleField')}</p>}
      {field && options.includes(field) && !isQueryableLogField(field) && <p role="alert">{t('invalidField')}</p>}
      {!field && <p className="text-muted-foreground">{t('selectHint')}</p>}
      {validField && (
        <Tabs value={mode} onValueChange={(value) => setMode(value as 'values' | 'stats')}>
          <TabsList aria-label={t('title')}>
            <TabsTrigger value="values">{t('values')}</TabsTrigger>
            <TabsTrigger value="stats">{t('numeric')}</TabsTrigger>
          </TabsList>
          <TabsContent value={mode} className="space-y-3">
          {mode === 'stats' && (
            <form className="space-y-2" onSubmit={(event) => {
              event.preventDefault()
              if ((groupBy && (!options.includes(groupBy) || !isQueryableLogField(groupBy))) || !(LOG_FIELD_TIME_BUCKETS as readonly string[]).includes(timeBucket)) {
                setFormError(true)
                return
              }
              setFormError(false)
              if (analysis?.groupBy === groupBy && analysis.timeBucket === timeBucket) void stats.refetch()
              else setAnalysis({ groupBy, timeBucket })
            }}>
              <p className="text-muted-foreground">{t('numericHint')}</p>
              <div className="flex flex-wrap items-end gap-2">
                <label htmlFor={`${id}-group`} className="flex min-w-0 flex-1 flex-col gap-1">
                  {t('groupBy')}
                  <NativeSelect size="sm" id={`${id}-group`} value={groupBy} onChange={(event) => setGroupBy(event.target.value)} className={selectClass}>
                    <NativeSelectOption value="">{t('ungrouped')}</NativeSelectOption>
                    {options.map((name) => <NativeSelectOption key={name} value={name}>{name}</NativeSelectOption>)}
                  </NativeSelect>
                </label>
                <label htmlFor={`${id}-bucket`} className="flex min-w-0 flex-1 flex-col gap-1">
                  {t('bucket')}
                  <NativeSelect size="sm" id={`${id}-bucket`} value={timeBucket} onChange={(event) => setTimeBucket(event.target.value)} className={selectClass}>
                    {LOG_FIELD_TIME_BUCKETS.map((value) => <NativeSelectOption key={value} value={value}>{value || t('noBucket')}</NativeSelectOption>)}
                  </NativeSelect>
                </label>
                <Button type="submit" size="sm" disabled={stats.isFetching}>{t('run')}</Button>
              </div>
              {formError && <p role="alert">{t('invalidField')}</p>}
              {!analysis && <p className="text-muted-foreground">{t('notRun')}</p>}
              {analysis && (analysis.groupBy !== groupBy || analysis.timeBucket !== timeBucket) && <p role="status">{t('draft')}</p>}
            </form>
          )}
          {hasRun && (
            <div className="min-h-28 space-y-2" data-testid="field-explorer-result" aria-busy={activeQuery.isFetching}>
              {activeQuery.isFetching && <p role="status">{t('loading')}</p>}
              {hasResponse && status.issues.length > 0 && (
                <div role={failed ? 'alert' : 'status'} className="rounded-md border p-2 text-muted-foreground" data-testid="field-explorer-status">
                  {status.issues.map((issue) => <p key={issue}>{t(issue)}</p>)}
                  {status.detail && <p className="break-words">{status.detail}</p>}
                  {status.reasons.length > 0 && <p className="break-words font-mono text-[11px]">{status.reasons.join(', ')}</p>}
                </div>
              )}
              {(invalidDimension || invalidStats) && <p role="alert">{t('invalidResponse')}</p>}
              {numericMissing && <p role="alert">{t('noNumbers')}</p>}
              {hasResponse && (status.issues.length > 0 || invalidDimension || invalidStats || numericMissing) && (
                <Button size="sm" variant="outline" disabled={activeQuery.isFetching} onClick={retry}>
                  <RefreshCw className="size-3" aria-hidden />{t('retry')}
                </Button>
              )}
              {mode === 'values' && !failed && values.length > 0 && (
                <div className="overflow-auto">
                  <Table className="w-full border-collapse text-left text-xs">
                    <TableHeader><TableRow className="border-b"><TableHead className="py-1 font-medium">{t('value')}</TableHead><TableHead className="p-1 text-right font-medium">{t(uncertain ? 'observedCount' : 'count')}</TableHead><TableHead><span className="sr-only">{t('include')} / {t('exclude')}</span></TableHead></TableRow></TableHeader>
                    <TableBody>{values.slice(0, 20).map((item, index) => (
                      <TableRow key={`${item.value}-${index}`} className="border-b border-border/50" data-testid="field-value-row">
                        <TableCell className="max-w-64 select-text whitespace-pre-wrap break-all py-1 font-mono">{item.value || t('emptyValue')}</TableCell>
                        <TableCell className="p-1 text-right tabular-nums">{uncertain ? '≈' : ''}{item.count.toLocaleString()}</TableCell>
                        <TableCell className="w-36 whitespace-nowrap p-1 text-right">
                          <Button size="sm" variant="ghost" aria-label={`${t('include')}: ${field}=${item.value}`} onClick={() => onFilter(fieldValueExpression(field, item.value))}>{t('include')}</Button>
                          <Button size="sm" variant="ghost" aria-label={`${t('exclude')}: ${field}=${item.value}`} onClick={() => onFilter(fieldValueExpression(field, item.value, true))}>{t('exclude')}</Button>
                        </TableCell>
                      </TableRow>
                    ))}</TableBody>
                  </Table>
                </div>
              )}
              {mode === 'values' && !failed && facets.isSuccess && !invalidDimension && values.length === 0 && <p>{t(uncertain ? 'noVerifiedValues' : 'emptyValues')}</p>}
              {mode === 'stats' && !failed && stats.isSuccess && points.length > 0 && (
                <div className="max-h-72 overflow-auto">
                  <Table className="w-full border-collapse text-right text-xs">
                    <TableHeader><TableRow className="border-b"><TableHead className="p-1 text-left">{t('group')}</TableHead>{analysis?.timeBucket && <TableHead className="p-1 text-left">{t('bucketTime')}</TableHead>}{(['count', 'sum', 'min', 'max', 'avg'] as const).map((metric) => <TableHead key={metric} className="p-1 font-medium">{metric}</TableHead>)}</TableRow></TableHeader>
                    <TableBody>{points.slice(0, ROW_LIMIT).map((point, index) => {
                      const metrics = fieldStatsValues(point)
                      const bucket = (point as typeof point & { time_bucket_utc?: string }).time_bucket_utc ?? point.dimensions?._time ?? t('missing')
                      return (
                        <TableRow key={index} className="border-b border-border/50" data-testid="field-stats-row">
                          <TableCell className="max-w-48 select-text whitespace-pre-wrap break-words p-1 text-left font-mono">{analysis?.groupBy ? point.dimensions?.[analysis.groupBy] ?? t('missing') : t('all')}</TableCell>
                          {analysis?.timeBucket && <TableCell className="p-1 text-left font-mono">{bucket}</TableCell>}
                          {(['count', 'sum', 'min', 'max', 'avg'] as const).map((metric) => <TableCell key={metric} className="p-1 tabular-nums" data-testid={`stats-${metric}`}>{metrics[metric] === undefined ? '—' : `${uncertain ? '≈' : ''}${metrics[metric].toLocaleString(undefined, { maximumFractionDigits: 6 })}`}</TableCell>)}
                        </TableRow>
                      )
                    })}</TableBody>
                  </Table>
                  {points.length > ROW_LIMIT && <p role="status">{t('showLimit', { limit: ROW_LIMIT })}</p>}
                </div>
              )}
              {mode === 'stats' && !failed && !invalidStats && stats.isSuccess && points.length === 0 && <p>{t(uncertain ? 'noVerifiedStats' : 'emptyStats')}</p>}
            </div>
          )}
          </TabsContent>
        </Tabs>
      )}
    </div>
  )
}
