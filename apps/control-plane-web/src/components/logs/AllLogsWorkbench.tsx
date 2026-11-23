import { useEffect, useMemo, useState } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { useQueries } from '@tanstack/react-query'
import { Download, RefreshCw } from 'lucide-react'
import api from '@/api/client'
import { useLogsFederation } from '@/api/logFederation'
import type { ProcessLogTail } from '@/api/nodeDiagnostics'
import type { LogEntry } from '@/lib/console/console-log-types'
import { filterProcessLogs, processLogEntries } from '@/lib/logs/process-log-text'
import { parseLogSource } from '@/lib/logs/logs-source-label'
import { downloadLogSnapshot, type LogSnapshotWindow } from '@/lib/logs/log-snapshot-export'
import { ROLE_PLATFORM_ADMIN } from '@/lib/shared/roles'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@jianmanager/ui/components/select'
import { LogTimeline } from './LogTimeline'
import { mergeLogEntries } from './AllLogsWorkbench.merge'

const WINDOW_LIMIT = 100
const MAX_NODE_SOURCES = 4
const INSTANCE_RANGE_MINUTES = 60

type NodeItem = { id: number; name: string; status: number | string }
type InstanceItem = { id: number; name: string; nodeId: number }

type TextTarget = {
  key: string
  source: 'control_plane' | 'worker'
  nodeId: number
  label: string
  path: string
  offline?: boolean
  skipped?: boolean
  missing?: boolean
}

type AcceptedText = {
  identity: string
  data: ProcessLogTail
  updatedAt: number
}

export interface AllLogsWorkbenchProps {
  nodes: NodeItem[]
  instances: InstanceItem[]
  initialNodeId: number | null
  initialInstanceId: number | null
  mapInstanceItems: (items: unknown[] | null | undefined) => LogEntry[]
  exportTarget?: HTMLElement | null
}

function isOnline(status: number | string): boolean {
  if (typeof status === 'number') return status === 1
  return ['online', 'ready', 'running', 'active', 'healthy'].includes(status.toLowerCase())
}

function stableNumber(value: string): number {
  let hash = 2166136261
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return hash | 0
}

/** Add the source identity to each id before combining windows. */
function withStableIds(items: readonly LogEntry[], sourceKey: string): LogEntry[] {
  const duplicateOrdinals = new Map<string, number>()
  return items.map((item) => {
    const base = `${sourceKey}|${item.id}|${item.nodeId}|${item.instanceId}|${item.time}|${item.message}`
    const ordinal = duplicateOrdinals.get(base) ?? 0
    duplicateOrdinals.set(base, ordinal + 1)
    return { ...item, id: stableNumber(`${base}|${ordinal}`) }
  })
}

function safeErrorNote(error: unknown): string {
  const status = (error as { response?: { status?: unknown } } | null)?.response?.status
  if (status === 429) return '429 · retry available'
  if (typeof status === 'number') return `HTTP ${status}`
  return 'request failed'
}

function localCopy(language: string, zh: string, en: string): string {
  return language.toLowerCase().startsWith('en') ? en : zh
}

export function AllLogsWorkbench({
  nodes,
  instances,
  initialNodeId,
  initialInstanceId,
  mapInstanceItems,
  exportTarget,
}: AllLogsWorkbenchProps) {
  const { t, i18n } = useTranslation()
  const language = i18n.language
  const english = language.startsWith('en')
  const role = useAuthStore((state) => state.role)
  const admin = role === ROLE_PLATFORM_ADMIN
  const onlineNodes = useMemo(() => nodes.filter((node) => isOnline(node.status)).sort((a, b) => a.id - b.id), [nodes])
  const [nodeId, setNodeId] = useState<number | null>(initialNodeId)
  const [instanceId, setInstanceId] = useState<number | null>(initialInstanceId)
  const [keyword, setKeyword] = useState('')
  const [level, setLevel] = useState('all')
  const [rangeMinutes, setRangeMinutes] = useState(INSTANCE_RANGE_MINUTES)
  const [rangeAnchor, setRangeAnchor] = useState(() => Date.now())
  const [paused, setPaused] = useState(false)
  const [acceptedText, setAcceptedText] = useState<Record<string, AcceptedText>>({})
  const [acceptedInstance, setAcceptedInstance] = useState<{ identity: string; data: LogEntry[]; updatedAt: number } | null>(null)

  const from = new Date(rangeAnchor - rangeMinutes * 60_000).toISOString()
  const to = new Date(rangeAnchor).toISOString()
  const instanceIdentity = `${nodeId ?? ''}:${instanceId ?? ''}:${from}:${to}`
  const instanceEnabled = admin
  const instanceQuery = useLogsFederation({
    view: 'node_instance',
    source: 'instance',
    ...(nodeId === null ? {} : { nodeId }),
    ...(instanceId === null ? {} : { instanceId }),
    from,
    to,
    page: 1,
    pageSize: WINDOW_LIMIT,
    limit: WINDOW_LIMIT,
  }, { enabled: instanceEnabled, refetchInterval: false })

  const targets = useMemo<TextTarget[]>(() => {
    if (!admin) return []
    const controlPlane: TextTarget = { key: 'control_plane', source: 'control_plane', nodeId: 0, label: english ? 'Platform · text' : '平台 · 文本', path: '/diagnostics/log' }
    if (nodeId !== null) {
      const node = nodes.find((candidate) => candidate.id === nodeId)
      return [controlPlane, {
        key: `worker:${nodeId}`,
        source: 'worker',
        nodeId,
        label: `${node?.name ?? `Node ${nodeId}`} · ${english ? 'text' : '文本'}`,
        path: `/nodes/${nodeId}/diagnostics/log`,
        offline: !node || !isOnline(node.status),
        missing: !node,
      }]
    }
    return [
      controlPlane,
      ...onlineNodes.slice(0, MAX_NODE_SOURCES).map((node) => ({ key: `worker:${node.id}`, source: 'worker' as const, nodeId: node.id, label: `${node.name} · ${english ? 'text' : '文本'}`, path: `/nodes/${node.id}/diagnostics/log` })),
      ...onlineNodes.slice(MAX_NODE_SOURCES).map((node) => ({ key: `worker:${node.id}`, source: 'worker' as const, nodeId: node.id, label: `${node.name} · ${english ? 'text' : '文本'}`, path: `/nodes/${node.id}/diagnostics/log`, skipped: true })),
      ...nodes.filter((node) => !isOnline(node.status)).map((node) => ({ key: `worker:${node.id}`, source: 'worker' as const, nodeId: node.id, label: node.name, path: `/nodes/${node.id}/diagnostics/log`, offline: true })),
    ]
  }, [admin, english, nodeId, nodes, onlineNodes])

  const textScopeIdentity = `${nodeId ?? ''}:${instanceId ?? ''}:${targets.map((target) => target.key).join(',')}`
  const textQueries = useQueries({
    queries: targets.map((target) => ({
      queryKey: ['allLogsTextWindow', textScopeIdentity, target.key, WINDOW_LIMIT],
      enabled: admin && !target.offline && !target.skipped,
      retry: false,
      refetchOnWindowFocus: false,
      refetchInterval: false,
      queryFn: async ({ signal }: { signal: AbortSignal }) => (await api.get<ProcessLogTail>(target.path, { params: { lines: WINDOW_LIMIT }, signal })).data,
    })),
  })
  useEffect(() => {
    if (paused) return
    let active = true
    queueMicrotask(() => {
      if (!active) return
      setAcceptedText((previous) => {
        const next = { ...previous }
        let changed = false
        textQueries.forEach((query, index) => {
          const target = targets[index]
          if (target && query.data && !query.isPlaceholderData) {
            const identity = `${textScopeIdentity}:${target.key}`
            if (next[target.key]?.identity !== identity || next[target.key]?.updatedAt !== query.dataUpdatedAt) {
              next[target.key] = { identity, data: query.data, updatedAt: query.dataUpdatedAt }
              changed = true
            }
          }
        })
        return changed ? next : previous
      })
    })
    return () => { active = false }
  }, [paused, targets, textQueries, textScopeIdentity])

  useEffect(() => {
    if (paused || !instanceQuery.data || instanceQuery.isPlaceholderData) return
    if (instanceQuery.dataUpdatedAt !== acceptedInstance?.updatedAt || acceptedInstance?.identity !== instanceIdentity) {
      let active = true
      queueMicrotask(() => {
        if (active) setAcceptedInstance({ identity: instanceIdentity, data: mapInstanceItems(instanceQuery.data?.response.items), updatedAt: instanceQuery.dataUpdatedAt })
      })
      return () => { active = false }
    }
  }, [acceptedInstance?.identity, acceptedInstance?.updatedAt, instanceIdentity, instanceQuery.data, instanceQuery.dataUpdatedAt, instanceQuery.isPlaceholderData, mapInstanceItems, paused])

  const textRows = textQueries.flatMap((_query, index) => {
    const target = targets[index]
    if (!target) return []
    const accepted = acceptedText[target.key]
    if (!accepted || accepted.identity !== `${textScopeIdentity}:${target.key}`) return []
    return withStableIds(processLogEntries(accepted.data.content ?? '', target.source, target.nodeId), target.key)
  })
  const instanceRows = (acceptedInstance?.identity === instanceIdentity ? acceptedInstance.data : []).map((item) => {
    const source = parseLogSource(item.sourceRef)
    if (source.kind !== 'instance' || source.id === null) return item
    const instance = instances.find((candidate) => candidate.id === source.id)
    return { ...item, instanceId: source.id, nodeId: instance?.nodeId ?? item.nodeId }
  })
  const loadedRows = mergeLogEntries([textRows, withStableIds(instanceRows, 'instance')])
  const visibleRows = filterProcessLogs(loadedRows, keyword, level === 'all' ? '' : level)

  const windows = useMemo<LogSnapshotWindow[]>(() => {
    const result: LogSnapshotWindow[] = targets.map((target, index) => {
      const query = textQueries[index]
      const cached = acceptedText[target.key]
      const accepted = cached?.identity === `${textScopeIdentity}:${target.key}` ? cached : undefined
      const rows = accepted ? processLogEntries(accepted.data.content ?? '', target.source, target.nodeId) : []
      const status: LogSnapshotWindow['status'] = target.skipped ? 'skipped' : target.offline ? 'offline' : query?.isError || !!accepted?.data.note ? 'failed' : !accepted ? 'loading' : 'ready'
      return {
        key: target.key,
        label: target.label,
        source: target.source,
        status,
        loadedRows: rows.length,
        limit: WINDOW_LIMIT,
        capturedAt: accepted ? new Date(accepted.updatedAt).toISOString() : undefined,
        truncated: accepted?.data.truncated,
        note: target.skipped ? 'scope limited to first 4 online nodes' : target.missing ? 'node id unknown; not read' : target.offline ? 'node offline; not read' : query?.isError ? safeErrorNote(query.error) : accepted?.data.note,
      }
    })
    const query = instanceQuery
    const response = query.data?.response
    const coverage = response?.coverage
    const incomplete = Boolean(
      query.data?.degraded
      || query.data?.federatedNotReady
      || response?.items == null
      || coverage?.complete !== true
    )
    const instanceNote = query.isError
      ? safeErrorNote(query.error)
      : query.data?.federatedNotReady
        ? 'VL unsupported or not ready'
        : coverage?.partialReasons?.length
          ? coverage.partialReasons.join(', ')
          : coverage?.pending ? (english ? 'coverage not verified' : '覆盖尚未校验')
            : response?.items == null ? (english ? 'instance result unavailable' : '实例查询暂不可用')
              : coverage?.complete !== true ? (english ? 'partial coverage' : '覆盖不完整') : undefined
    result.push({
      key: 'instance',
      label: instanceId === null ? (english ? 'Instances · VL' : '实例 · VL') : `${instances.find((item) => item.id === instanceId)?.name ?? `Instance ${instanceId}`} · VL`,
      source: 'instance',
      status: query.isError ? 'failed' : query.isPending && !acceptedInstance ? 'loading' : incomplete ? 'failed' : 'ready',
      loadedRows: instanceRows.length,
      limit: WINDOW_LIMIT,
      capturedAt: acceptedInstance?.updatedAt ? new Date(acceptedInstance.updatedAt).toISOString() : undefined,
      from,
      to,
      truncated: coverage?.partialReasons?.includes('TRUNCATED') === true,
      note: instanceNote,
    })
    return result
  }, [acceptedInstance, acceptedText, english, from, instanceId, instanceQuery, instances, targets, textQueries, textScopeIdentity, to, instanceRows.length])

  const failureCount = windows.filter((window) => window.status !== 'ready' || window.note).length
  const sourceLabelOf = (item: LogEntry) => {
    if (item.source === 'control_plane') return english ? 'Platform · text' : '平台 · 文本'
    if (item.source === 'worker') return `${nodes.find((node) => node.id === item.nodeId)?.name ?? `Node ${item.nodeId}`} · ${english ? 'text' : '文本'}`
    return `${instances.find((instance) => instance.id === item.instanceId)?.name ?? `Instance ${item.instanceId}`}·VL`
  }
  const refresh = () => {
    if (paused) return
    textQueries.forEach((query, index) => {
      const target = targets[index]
      if (target && !target.offline && !target.skipped) void query.refetch()
    })
    if (instanceEnabled) {
      const next = Date.now()
      if (next === rangeAnchor) void instanceQuery.refetch()
      else setRangeAnchor(next)
    }
  }
  const canExport = admin && visibleRows.length > 0 && !paused && !instanceQuery.isFetching && !textQueries.some((query) => query.isFetching)
  const exportButton = (
    <Button size="sm" variant="outline" className="shrink-0 self-end" disabled={!canExport} data-testid="all-logs-export" onClick={() => downloadLogSnapshot(visibleRows, windows, 'all-logs')}><Download className="size-3.5" />{t('logs.export')}</Button>
  )

  if (!admin) {
    return <div data-testid="all-logs-workbench" className="rounded-md border p-4 text-sm text-muted-foreground">{localCopy(language, '仅平台管理员可查看融合日志。', 'Only platform administrators can view federated logs.')}</div>
  }

  return (
    <div data-testid="all-logs-workbench" className="flex min-h-0 flex-1 flex-col gap-2">
      {exportTarget === undefined ? exportButton : exportTarget ? createPortal(exportButton, exportTarget) : null}

      <div className="grid shrink-0 grid-cols-2 gap-2 md:grid-cols-4">
        {[
          [localCopy(language, '已加载汇总', 'Loaded rows'), visibleRows.length],
          [localCopy(language, '实例当前页', 'Instance current page'), instanceRows.length],
          [localCopy(language, '文本窗口', 'Text windows'), windows.filter((window) => window.source !== 'instance').length],
          [localCopy(language, '失败或受限来源', 'Failed or limited'), failureCount],
        ].map(([label, value]) => <div key={String(label)} className="rounded-md border bg-card/50 p-2 text-xs"><p className="text-muted-foreground">{label}</p><p className="mt-1 text-lg tabular-nums">{value}</p></div>)}
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-2 rounded-md border bg-card p-2" data-testid="all-logs-toolbar">
        <Input className="h-9 min-w-[190px] flex-1" value={keyword} onChange={(event) => setKeyword(event.target.value)} placeholder={localCopy(language, '搜索已加载文本', 'Search loaded text')} aria-label={localCopy(language, '搜索已加载文本', 'Search loaded text')} />
        <Select value={level} onValueChange={setLevel}><SelectTrigger className="h-9 w-28"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">{localCopy(language, '全部级别', 'All levels')}</SelectItem>{['error', 'warn', 'info', 'debug', 'unknown'].map((value) => <SelectItem key={value} value={value}>{value}</SelectItem>)}</SelectContent></Select>
        <Select value={nodeId === null ? '__all__' : String(nodeId)} onValueChange={(value) => { if (!paused) setNodeId(value === '__all__' ? null : Number(value)) }}><SelectTrigger className="h-9 w-36" data-testid="all-logs-node-select"><SelectValue placeholder={localCopy(language, '节点', 'Node')} /></SelectTrigger><SelectContent><SelectItem value="__all__">{localCopy(language, '全部在线节点', 'All online nodes')}</SelectItem>{nodes.map((node) => <SelectItem key={node.id} value={String(node.id)}>{node.name}</SelectItem>)}</SelectContent></Select>
        <Select value={instanceId === null ? '__all__' : String(instanceId)} onValueChange={(value) => { if (!paused) setInstanceId(value === '__all__' ? null : Number(value)) }}><SelectTrigger className="h-9 w-36" data-testid="all-logs-instance-select"><SelectValue placeholder={localCopy(language, '实例', 'Instance')} /></SelectTrigger><SelectContent><SelectItem value="__all__">{localCopy(language, '全部实例', 'All instances')}</SelectItem>{instances.map((instance) => <SelectItem key={instance.id} value={String(instance.id)}>{instance.name}</SelectItem>)}</SelectContent></Select>
        <Select value={String(rangeMinutes)} onValueChange={(value) => { if (!paused) { setRangeMinutes(Number(value)); setRangeAnchor(Date.now()) } }}><SelectTrigger className="h-9 w-28" data-testid="all-logs-range-select"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="15">15m</SelectItem><SelectItem value="60">1h</SelectItem><SelectItem value="1440">24h</SelectItem></SelectContent></Select>
        <Button size="sm" variant="outline" data-testid="all-logs-refresh" disabled={paused || textQueries.some((query) => query.isFetching) || instanceQuery.isFetching} onClick={refresh}><RefreshCw className="size-3.5" />{localCopy(language, '刷新', 'Refresh')}</Button>
        <details className="min-w-0 basis-full rounded-md border bg-muted/20" data-testid="all-logs-source-status">
          <summary className="cursor-pointer px-2 py-1 text-sm" title={english ? `Text: latest ${WINDOW_LIMIT} rows per source; VL: current page, ${new Date(from).toLocaleString()} – ${new Date(to).toLocaleString()}.` : `文本每源最近 ${WINDOW_LIMIT} 行；实例 VL 当前页：${new Date(from).toLocaleString()} – ${new Date(to).toLocaleString()}。`}>
            {localCopy(language, '来源状态', 'Source status')} · {windows.length}
            {failureCount > 0 && <span className="ml-2 text-status-warning">{localCopy(language, `${failureCount} 个来源受限`, `${failureCount} limited`)}</span>}
          </summary>
          <div className="max-h-36 overflow-y-auto border-t">
            {windows.map((window) => <div key={window.key} className="flex items-center justify-between gap-2 px-3 py-1.5 text-sm"><span className="truncate">{window.label}</span><span className={window.status === 'ready' && !window.note ? 'text-muted-foreground' : 'text-status-warning'}>{english ? window.status : ({ ready: '已读取', loading: '读取中', failed: '读取失败', offline: '离线，未读取', skipped: '超出窗口，未读取' }[window.status])} · {window.loadedRows}/{window.limit}{window.truncated ? (english ? ' · tail truncated' : ' · 尾部截断') : ''}{window.note ? ` · ${window.note}` : ''}</span></div>)}
            {nodeId === null && onlineNodes.length > MAX_NODE_SOURCES && <div className="px-3 py-1 text-sm text-status-warning">{localCopy(language, `在线节点超过 ${MAX_NODE_SOURCES} 个，范围受限`, `More than ${MAX_NODE_SOURCES} online nodes; scope limited`)}</div>}
          </div>
        </details>
      </div>

      <div className="min-h-0 flex-1 overflow-hidden rounded-md border bg-card">
        <div className="flex min-h-0 h-full flex-col">

          <div className="flex min-h-0 flex-1 flex-col"><LogTimeline items={visibleRows} total={visibleRows.length} follow={false} filterKey={`${instanceIdentity}:${textScopeIdentity}:${keyword}:${level}`} loading={windows.some((window) => window.status === 'loading')} emptySuccessAllowed={windows.length > 0 && windows.every((window) => window.status === 'ready')} emptyReason="no_data" emptyHint={localCopy(language, '仅显示已加载窗口；文本是文件尾部，实例是 VL 当前页。', 'Loaded windows only; text is a file tail and instances are the current VL page.')} highlightKeyword={keyword} sourceLabelOf={sourceLabelOf} onInteractionPauseChange={setPaused} /></div>
          <footer className="flex h-10 shrink-0 items-center justify-between gap-2 border-t px-3 text-xs text-muted-foreground" data-testid="all-logs-footer"><span>{localCopy(language, `${visibleRows.length} 行`, `${visibleRows.length} rows`)}</span></footer>
        </div>
      </div>
    </div>
  )
}
