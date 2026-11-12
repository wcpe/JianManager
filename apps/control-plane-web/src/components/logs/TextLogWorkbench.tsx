import { useMemo, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Pause, Play, RefreshCw } from 'lucide-react'
import api from '@/api/client'
import type { ProcessLogTail } from '@/api/nodeDiagnostics'
import { filterProcessLogs, processLogEntries } from '@/lib/logs/process-log-text'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@jianmanager/ui/components/select'
import { LogTimeline } from './LogTimeline'

/** A bounded file window shared by CP and Worker diagnostics; never queries VL. */
export function TextLogWorkbench({ kind, nodeId, nodes, onNodeChange, sourceControl }: {
  kind: 'control_plane' | 'worker'
  nodeId: number | null
  nodes: { id: number; name: string; status: number }[]
  onNodeChange: (nodeId: number | null) => void
  sourceControl: ReactNode
}) {
  const { t } = useTranslation()
  const [lines, setLines] = useState(200)
  const [keyword, setKeyword] = useState('')
  const [level, setLevel] = useState('all')
  const [follow, setFollow] = useState(false)
  const [selecting, setSelecting] = useState(false)
  const identity = `${kind}:${nodeId ?? ''}:${lines}`
  const [accepted, setAccepted] = useState<{ identity: string; data: ProcessLogTail; at: number } | null>(null)
  const ready = kind === 'control_plane' || nodeId !== null
  const paused = selecting
  const query = useQuery({
    queryKey: ['logsTextWindow', kind, nodeId, lines],
    enabled: ready,
    retry: false,
    refetchOnWindowFocus: false,
    refetchInterval: follow && !paused ? 3000 : false,
    refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
      const path = kind === 'control_plane' ? '/diagnostics/log' : `/nodes/${nodeId}/diagnostics/log`
      return (await api.get<ProcessLogTail>(path, { params: { lines }, signal })).data
    },
  })
  // A response arriving during a drag/read must not replace the selected DOM.
  if (!paused && query.data && (accepted?.identity !== identity || accepted.at !== query.dataUpdatedAt)) {
    setAccepted({ identity, data: query.data, at: query.dataUpdatedAt })
  }
  const snapshot = accepted?.identity === identity ? accepted : null
  const allRows = useMemo(() => processLogEntries(snapshot?.data.content ?? '', kind, nodeId ?? 0), [snapshot?.data.content, kind, nodeId])
  const rows = useMemo(() => filterProcessLogs(allRows, keyword, level === 'all' ? '' : level), [allRows, keyword, level])
  const sourceName = kind === 'control_plane' ? t('logs.source_control_plane') : nodes.find((node) => node.id === nodeId)?.name ?? t('logsText.chooseNode')
  const errorMessage = query.error && typeof query.error === 'object'
    ? (query.error as { response?: { data?: { message?: string } }; message?: string }).response?.data?.message ?? query.error.message
    : ''

  return <>
    <div data-testid="logs-insights-slot" className="grid h-[104px] shrink-0 grid-cols-[repeat(3,minmax(180px,1fr))] gap-2 overflow-x-auto overflow-y-hidden">
      <div className="min-w-[180px] rounded-md border bg-card/50 p-3 text-xs"><p className="text-muted-foreground">{t('logsText.window')}</p><p className="mt-2 text-lg tabular-nums">{allRows.length} / {lines}</p><p className="truncate text-muted-foreground">{t('logsText.loadedOnly')}</p></div>
      <div className="min-w-[180px] rounded-md border bg-card/50 p-3 text-xs"><p className="text-muted-foreground">{t('logsText.file')}</p><p className="mt-2 truncate" title={snapshot?.data.path}>{snapshot?.data.path || '—'}</p><p className="mt-1 tabular-nums text-muted-foreground">{snapshot ? `${snapshot.data.fileBytes.toLocaleString()} B` : '—'}</p></div>
      <div className="min-w-[180px] rounded-md border bg-card/50 p-3 text-xs"><p className="text-muted-foreground">{t('logsText.updated')}</p><p className="mt-2 tabular-nums">{snapshot ? new Date(snapshot.at).toLocaleTimeString() : '—'}</p><p className="mt-1 truncate text-muted-foreground">{t(follow ? paused ? 'logsText.reading' : 'logsText.following' : 'logsText.snapshot')}</p></div>
    </div>
    <div className="flex h-[116px] shrink-0 flex-col gap-2 overflow-hidden rounded-md border bg-card p-2" data-testid="logs-toolbar">
      <div className="flex min-w-0 items-center gap-2 overflow-x-auto">
        {sourceControl}
        <Select value={nodeId === null ? '__none__' : String(nodeId)} disabled={kind === 'control_plane'} onValueChange={(value) => { setSelecting(false); onNodeChange(value === '__none__' ? null : Number(value)) }}>
          <SelectTrigger className="h-9 w-40 shrink-0" data-testid="logs-node-select"><SelectValue placeholder={t('logsText.chooseNode')} /></SelectTrigger>
          <SelectContent><SelectItem value="__none__">{t('logsText.chooseNode')}</SelectItem>{nodes.map((node) => <SelectItem key={node.id} value={String(node.id)}>{node.name}</SelectItem>)}</SelectContent>
        </Select>
        <Input className="h-9 min-w-[220px] flex-1" value={keyword} onChange={(event) => setKeyword(event.target.value)} placeholder={t('logsText.search')} aria-label={t('logsText.search')} />
      </div>
      <div className="flex min-w-0 flex-1 items-center gap-2 overflow-x-auto">
        <Select value={level} onValueChange={setLevel}>
          <SelectTrigger className="h-9 w-28 shrink-0" aria-label={t('logs.allLevels')}><SelectValue /></SelectTrigger>
          <SelectContent><SelectItem value="all">{t('logs.allLevels')}</SelectItem>{['error', 'warn', 'info', 'debug', 'unknown'].map((value) => <SelectItem key={value} value={value}>{t(`logs.level_${value}`, value)}</SelectItem>)}</SelectContent>
        </Select>
        <Select value={String(lines)} onValueChange={(value) => { setSelecting(false); setLines(Number(value)) }}>
          <SelectTrigger className="h-9 w-32 shrink-0" aria-label={t('logsText.window')}><SelectValue /></SelectTrigger>
          <SelectContent>{[50, 100, 200].map((size) => <SelectItem key={size} value={String(size)}>{t('logsText.lineWindow', { count: size })}</SelectItem>)}</SelectContent>
        </Select>
        <Button size="sm" variant="outline" disabled={!ready || query.isFetching || selecting} onClick={() => { void query.refetch() }}><RefreshCw className="size-3.5" />{t('common.refresh')}</Button>
        <Button size="sm" variant="outline" aria-pressed={follow} disabled={!ready} onClick={() => { setFollow((value) => !value) }}>{follow ? <Pause className="size-3.5" /> : <Play className="size-3.5" />}{t('logs.follow')}</Button>
      </div>
    </div>
    <div className="flex h-11 shrink-0 items-center overflow-hidden text-xs text-muted-foreground" role="status" data-testid="logs-text-status">
      {!ready ? <span>{t('logsText.chooseNode')}</span>
        : query.isError ? <span role="alert" className="truncate text-destructive">{t('logs.loadError')} · {errorMessage}</span>
        : snapshot?.data.note ? <span>{snapshot.data.note}</span>
          : <span>{t('logsText.loadedOnly')}{snapshot?.data.truncated ? ` · ${t('logsText.truncated')}` : ''}{query.isFetching ? ` · ${t('common.loading')}` : ''}{paused && follow ? ` · ${t('logsText.reading')}` : ''}</span>}
    </div>
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-md border bg-card" data-testid="logs-text-workbench">
      <div className="flex min-h-0 flex-1 flex-col">
        <LogTimeline onInteractionPauseChange={setSelecting} items={rows} total={rows.length} follow={follow && !paused} filterKey={`${identity}:${keyword}:${level}`} loading={ready && query.isLoading} emptySuccessAllowed={ready && query.isSuccess && !snapshot?.data.note} emptyReason={ready ? 'no_data' : 'choose_node'} emptyHint={!ready ? t('logsText.chooseNode') : snapshot?.data.note || t('logsText.noMatch')} highlightKeyword={keyword} sourceLabelOf={() => sourceName} />
      </div>
      <footer className="flex h-10 shrink-0 items-center justify-between gap-2 border-t px-3 text-xs text-muted-foreground" data-testid="logs-text-footer">
        <span>{t('logsText.visible', { count: rows.length, total: allRows.length })}</span><span className="truncate">{t('logsText.noHistory')}</span>
      </footer>
    </div>
  </>
}
