import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import ClientDistExportButton from '@/components/ClientDistExportButton'
import { useClientDistSecurityLogs, type ClientDistSecurityLogType } from '@/api/clientDistSecurity'
import {
  useClientDistEventDetail,
  useClientDistEventSearch,
  type ClientDistEvent,
  type ClientDistEventDetail,
} from '@/api/clientDistEvents'
import { buildClientDistHref, readClientDistQuery, updateClientDistQuery } from '@/lib/client-dist-query'
import {
  OPS_ALL,
  ResultBadge,
  fmtTime,
  kindLabel,
  targetOf,
  type RuntimeLink,
} from './ops-shared'

/** 全量日志视图取值集合（request 为独立加厚视图，其余走安全聚合流）。 */
const LOG_TYPE_VALUES = ['all', 'hello', 'risk', 'action', 'request', 'runtime', 'telemetry'] as const
type LogTypeValue = (typeof LOG_TYPE_VALUES)[number]

function isLogTypeValue(v: string | null): v is LogTypeValue {
  return !!v && (LOG_TYPE_VALUES as readonly string[]).includes(v)
}

/** 日志类型 → 本地化标签。 */
function logTypeLabel(value: ClientDistSecurityLogType | 'all', t: (key: string) => string): string {
  switch (value) {
    case 'all':
      return t('clientDistOps.logs.typeAll')
    case 'hello':
      return t('clientDistOps.logs.typeHello')
    case 'risk':
      return t('clientDistOps.logs.typeRisk')
    case 'action':
      return t('clientDistOps.logs.typeAction')
    case 'request':
      return t('clientDistOps.logs.typeRequest')
    case 'runtime':
      return t('clientDistOps.logs.typeRuntime')
    case 'telemetry':
      return t('clientDistOps.logs.typeTelemetry')
    default:
      return value
  }
}

/** 全量日志类型选择器（视图切换：request=加厚请求日志；其余=安全聚合 6 类型）。 */
function LogTypeSelect({ value, onChange }: { value: LogTypeValue; onChange: (v: LogTypeValue) => void }) {
  const { t } = useTranslation()
  return (
    <Select value={value} onValueChange={(v) => onChange(v as LogTypeValue)}>
      <SelectTrigger size="sm" className="w-36"><SelectValue /></SelectTrigger>
      <SelectContent>
        {LOG_TYPE_VALUES.map((key) => (
          <SelectItem key={key} value={key}>{logTypeLabel(key, t)}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/**
 * 页面 B · 全量日志 Tab（FR-430 / ADR-088 去重合并的**唯一入口**）。
 *
 * 按 `type` 双视图：
 * - `type=request`（缺省）：加厚请求表 + 脱敏详情（`useClientDistEventSearch` + `useClientDistEventDetail`，保留 FR-357/FR-265）。
 * - `type=all` 或其余 5 类：安全聚合多类型表（`useClientDistSecurityLogs`，服务端聚合 hello/risk/action/request/runtime/telemetry）。
 *
 * 两 hook 均**保留**，仅从「两页各一份」收敛为「一个 Tab 内两种视图」。
 */
export default function ClientDistLogsTab({
  channelId,
  range,
  enabled,
  link,
  onClearLink,
}: {
  channelId?: string
  range: string
  enabled: boolean
  link: RuntimeLink
  onClearLink: () => void
}) {
  const [searchParams, setSearchParams] = useSearchParams()
  const rawType = searchParams.get('type')
  // 缺省进入 logs 无 type → request（等同旧监控体验）。
  const type: LogTypeValue = isLogTypeValue(rawType) ? rawType : 'request'
  const setType = (v: LogTypeValue) => {
    setSearchParams(updateClientDistQuery(searchParams, { type: v }), { replace: true })
  }

  if (type === 'request') {
    return <RequestLogsView channelId={channelId} range={range} enabled={enabled} link={link} onClearLink={onClearLink} type={type} onTypeChange={setType} />
  }
  return <AggregateLogsView channelId={channelId} range={range} type={type} onTypeChange={setType} />
}

/** 加厚请求日志视图（迁自旧监控页 `LogsTab`）。 */
function RequestLogsView({
  channelId,
  range,
  enabled,
  link,
  onClearLink,
  type,
  onTypeChange,
}: {
  channelId?: string
  range: string
  enabled: boolean
  link: RuntimeLink
  onClearLink: () => void
  type: LogTypeValue
  onTypeChange: (v: LogTypeValue) => void
}) {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const [outcome, setOutcome] = useState<string>(OPS_ALL)
  const [kind, setKind] = useState<string>(OPS_ALL)
  const [detailId, setDetailId] = useState<number | null>(null)
  const { data, isError, isLoading } = useClientDistEventSearch({
    channelId,
    ...link,
    kind: kind === OPS_ALL ? undefined : kind,
    outcome: outcome === OPS_ALL ? '' : (outcome as 'success' | 'failure'),
    page: 1,
    pageSize: 100,
    enabled,
  })
  const events = data?.items ?? []
  const hasLink = Object.values(link).some((v) => v !== undefined && v !== '')
  const securityHref = buildClientDistHref('/client-dist-ops', searchParams, { tab: 'logs', type: 'all' })
  const channelHref = buildClientDistHref('/client-channels', searchParams, { tab: 'stats' })

  const filters = (
    <div className="flex flex-wrap items-center gap-2">
      <LogTypeSelect value={type} onChange={onTypeChange} />
      <Select value={outcome} onValueChange={setOutcome}>
        <SelectTrigger size="sm" className="w-32"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value={OPS_ALL}>{t('clientDistOps.outcomeAll')}</SelectItem>
          <SelectItem value="success">{t('clientDistOps.outcomeSuccess')}</SelectItem>
          <SelectItem value="failure">{t('clientDistOps.outcomeFailure')}</SelectItem>
        </SelectContent>
      </Select>
      <Select value={kind} onValueChange={setKind}>
        <SelectTrigger size="sm" className="w-32"><SelectValue /></SelectTrigger>
        <SelectContent>
          <SelectItem value={OPS_ALL}>{t('clientDistOps.allKinds')}</SelectItem>
          <SelectItem value="manifest">{t('clientDistOps.kindManifest')}</SelectItem>
          <SelectItem value="artifact">{t('clientDistOps.kindArtifact')}</SelectItem>
        </SelectContent>
      </Select>
      {hasLink && <Button type="button" variant="outline" size="sm" onClick={onClearLink}>{t('clientDistOps.clearLink')}</Button>}
      <ClientDistExportButton
        kind="dist-events"
        filters={{
          channelId,
          range,
          machineId: link.machineId,
          version: link.version,
          errCode: link.errCode,
          ip: link.ip,
          eventKind: kind === OPS_ALL ? undefined : (kind as 'manifest' | 'artifact'),
          outcome: outcome === OPS_ALL ? undefined : outcome,
        }}
      />
    </div>
  )

  return (
    <>
      <Panel title={t('clientDistOps.logsTitle')} actions={filters}>
        {hasLink && (
          <div className="mb-3 flex flex-wrap items-center gap-2 rounded-lg border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
            <span>{t('clientDistOps.linkedFilter')}</span>
            {link.machineId && <Badge variant="outline" className="font-mono">machine={link.machineId}</Badge>}
            {link.runtimeVersion !== undefined && <Badge variant="outline">version=v{link.runtimeVersion}</Badge>}
            {link.version !== undefined && <Badge variant="outline">version=v{link.version}</Badge>}
            {link.coreVersion && <Badge variant="outline">core={link.coreVersion}</Badge>}
            {link.platform && <Badge variant="outline">platform={link.platform}</Badge>}
            {link.lag !== undefined && <Badge variant="outline">lag={link.lag}</Badge>}
            {link.errCode && <Badge variant="outline" className="font-mono">errCode={link.errCode}</Badge>}
            {link.ip && <Badge variant="outline" className="font-mono">ip={link.ip}</Badge>}
            <Link className="font-medium text-primary hover:underline" to={securityHref}>{t('clientDistOps.logs.openSecurityCenter')}</Link>
            <Link className="font-medium text-primary hover:underline" to={channelHref}>{t('clientDistOps.logs.openChannelWorkbench')}</Link>
          </div>
        )}
        {isError ? (
          <p className="py-10 text-center text-sm text-muted-foreground">{t('clientDistOps.eventsError')}</p>
        ) : events.length === 0 ? (
          <p className="py-10 text-center text-sm text-muted-foreground">{isLoading ? t('common.loading') : t('clientDistOps.eventsEmpty')}</p>
        ) : (
          <EventTable events={events} onDetail={setDetailId} />
        )}
      </Panel>
      <EventDetailDialog id={detailId} open={!!detailId} onOpenChange={(open) => !open && setDetailId(null)} />
    </>
  )
}

/** 安全聚合多类型视图（迁自安全页 `LogsTab`）。 */
function AggregateLogsView({
  channelId,
  range,
  type,
  onTypeChange,
}: {
  channelId?: string
  range: string
  type: LogTypeValue
  onTypeChange: (v: LogTypeValue) => void
}) {
  const { t } = useTranslation()
  const [searchParams, setSearchParams] = useSearchParams()
  const query = readClientDistQuery(searchParams)
  const updateQuery = (patch: Partial<Record<'channelId' | 'machineId' | 'ip' | 'errCode', string | null>>) => {
    setSearchParams(updateClientDistQuery(searchParams, patch), { replace: true })
  }
  const [playerName, setPlayerName] = useState('')
  const { data, isError, isLoading } = useClientDistSecurityLogs({
    type,
    channelId: query.channelId ?? channelId,
    machineId: query.machineId,
    playerName: playerName || undefined,
    ip: query.ip,
    errCode: query.errCode,
    page: 1,
    pageSize: 100,
  })
  const items = data?.items ?? []

  const filters = (
    <div className="flex flex-wrap gap-2">
      <LogTypeSelect value={type} onChange={onTypeChange} />
      <Input className="w-36" placeholder={t('clientDistOps.logs.phChannel')} value={query.channelId ?? ''} onChange={(e) => updateQuery({ channelId: e.target.value || null })} />
      <Input className="w-40" placeholder={t('clientDistOps.logs.phMachine')} value={query.machineId ?? ''} onChange={(e) => updateQuery({ machineId: e.target.value || null })} />
      <Input className="w-32" placeholder={t('clientDistOps.logs.phPlayer')} value={playerName} onChange={(e) => setPlayerName(e.target.value)} />
      <Input className="w-36" placeholder={t('clientDistOps.logs.phIp')} value={query.ip ?? ''} onChange={(e) => updateQuery({ ip: e.target.value || null })} />
      <Input className="w-40" placeholder={t('clientDistOps.logs.phErrCode')} value={query.errCode ?? ''} onChange={(e) => updateQuery({ errCode: e.target.value || null })} />
      <ClientDistExportButton
        kind="security-logs"
        filters={{
          range,
          type: type === 'all' ? undefined : type,
          channelId: query.channelId ?? channelId,
          machineId: query.machineId,
          playerName: playerName || undefined,
          ip: query.ip,
          errCode: query.errCode,
        }}
      />
    </div>
  )

  return (
    <Panel title={t('clientDistOps.logs.title')} actions={filters}>
      {isError ? (
        <p className="py-10 text-center text-sm text-muted-foreground">{t('clientDistOps.logs.error')}</p>
      ) : items.length === 0 ? (
        <p className="py-10 text-center text-sm text-muted-foreground">{isLoading ? t('common.loading') : t('clientDistOps.logs.empty')}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('clientDistOps.logs.colTime')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colType')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colTitle')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colObject')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colStatusErr')}</TableHead>
              <TableHead>{t('clientDistOps.logs.colDetail')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((item) => (
              <TableRow key={item.id}>
                <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(item.createdAt)}</TableCell>
                <TableCell><Badge variant="outline">{logTypeLabel(item.type, t)}</Badge></TableCell>
                <TableCell className="font-medium">{item.title || '—'}</TableCell>
                <TableCell className="max-w-64 text-xs">
                  <div className="truncate">{t('clientDistOps.logs.prefixChannel')}{item.channelId || '—'}</div>
                  <div className="truncate text-muted-foreground">{t('clientDistOps.logs.prefixPlayer')}{item.playerName || '—'} · {t('clientDistOps.logs.prefixIp')}{item.ip || '—'}</div>
                  <div className="truncate text-muted-foreground">{t('clientDistOps.logs.prefixMachine')}{item.machineId || '—'}</div>
                </TableCell>
                <TableCell>
                  <div>{item.status || '—'}</div>
                  <div className="text-xs text-muted-foreground">{item.errCode || '—'}</div>
                </TableCell>
                <TableCell className="max-w-96">
                  <pre className="max-h-40 overflow-auto rounded bg-muted p-2 text-xs text-muted-foreground">{JSON.stringify(item.detail ?? {}, null, 2)}</pre>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

/** 请求日志表（加厚列，FR-357）。 */
function EventTable({ events, onDetail }: { events: ClientDistEvent[]; onDetail: (id: number) => void }) {
  const { t } = useTranslation()
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>{t('clientDistOps.colTime')}</TableHead>
          <TableHead>{t('clientDistOps.colChannel')}</TableHead>
          <TableHead>{t('clientDistOps.colPlayer', '玩家名')}</TableHead>
          <TableHead>{t('clientDistOps.colMachine')}</TableHead>
          <TableHead>{t('clientDistOps.colCoreVersion', 'Core 版本')}</TableHead>
          <TableHead>{t('clientDistOps.colKind')}</TableHead>
          <TableHead>{t('clientDistOps.colTarget')}</TableHead>
          <TableHead>{t('clientDistOps.colIp')}</TableHead>
          <TableHead>{t('clientDistOps.colBytes', '字节')}</TableHead>
          <TableHead>{t('clientDistOps.colDuration', '耗时')}</TableHead>
          <TableHead>{t('clientDistOps.colStatus')}</TableHead>
          <TableHead>{t('clientDistOps.colResult')}</TableHead>
          <TableHead>{t('clientDistOps.colErrCode')}</TableHead>
          <TableHead>{t('clientDistOps.colDetail')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((e) => (
          <TableRow key={e.id}>
            <TableCell className="tabular-nums text-muted-foreground">{fmtTime(e.createdAt)}</TableCell>
            <TableCell>{e.channelId || '—'}</TableCell>
            <TableCell className="text-xs">{e.playerName || '—'}</TableCell>
            <TableCell className="font-mono text-xs">{e.machineId || '—'}</TableCell>
            <TableCell className="font-mono text-xs">{e.coreVersion || '—'}</TableCell>
            <TableCell>{kindLabel(e.kind, t)}</TableCell>
            <TableCell className="font-mono text-xs">{targetOf(e)}</TableCell>
            <TableCell className="tabular-nums">{e.ip || '—'}</TableCell>
            <TableCell className="tabular-nums">{e.bytes}</TableCell>
            <TableCell className="tabular-nums">{e.durationMs}ms</TableCell>
            <TableCell className="tabular-nums">{e.status}</TableCell>
            <TableCell><ResultBadge status={e.status} /></TableCell>
            <TableCell>{e.errCode ? <Badge variant="outline" className="font-mono text-xs">{e.errCode}</Badge> : <span className="text-muted-foreground">—</span>}</TableCell>
            <TableCell><Button type="button" variant="outline" size="xs" onClick={() => onDetail(e.id)}>{t('clientDistOps.viewDetail')}</Button></TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

/** 单条请求脱敏详情弹窗（FR-265）。 */
function EventDetailDialog({ id, open, onOpenChange }: { id: number | null; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useClientDistEventDetail(id, open)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('clientDistOps.detailTitle')}</DialogTitle>
          <DialogDescription>{t('clientDistOps.detailHint')}</DialogDescription>
        </DialogHeader>
        {isError ? <p className="text-sm text-muted-foreground">{t('clientDistOps.detailError')}</p> : null}
        {isLoading ? <p className="text-sm text-muted-foreground">{t('common.loading')}</p> : data ? <EventDetailBody detail={data} /> : null}
      </DialogContent>
    </Dialog>
  )
}

function EventDetailBody({ detail }: { detail: ClientDistEventDetail }) {
  const { t } = useTranslation()
  const failed = detail.status >= 400 || !!detail.errCode
  const fmtDur = (ms?: number) => (!ms && ms !== 0 ? '—' : ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`)
  return (
    <div className="space-y-4 text-sm">
      <div className={`rounded-lg border p-3 ${failed ? 'border-destructive/40 bg-destructive/5' : 'border-emerald-500/40 bg-emerald-500/5'}`}>
        <div className="flex items-center gap-2">
          <Badge variant={failed ? 'destructive' : 'default'}>{detail.status}</Badge>
          <span className="font-medium">{detail.kind}</span>
          {detail.errCode ? <Badge variant="outline" className="font-mono">{detail.errCode}</Badge> : null}
          <span className="ml-auto text-xs text-muted-foreground">{fmtTime(detail.createdAt)}</span>
        </div>
        {detail.errReason ? <p className="mt-2 text-xs text-muted-foreground">{detail.errReason}</p> : null}
      </div>

      <section>
        <h4 className="mb-2 text-xs font-semibold">{t('clientDistOps.detailIdentity', '身份与来源')}</h4>
        <div className="grid grid-cols-2 gap-x-4 gap-y-2 rounded-lg border p-3 text-xs sm:grid-cols-3">
          <DetailLine label={t('clientDistOps.colChannel')} value={detail.channelId || '—'} />
          <DetailLine label={t('clientDistOps.colIp')} value={detail.ip || '—'} mono />
          <DetailLine label={t('clientDistOps.colMachine', '机器 ID')} value={detail.machineId || '—'} mono breakAll />
          <DetailLine label={t('clientDistOps.colPlayer', '玩家')} value={detail.playerName || '—'} />
          <DetailLine label={t('clientDistOps.colCore', 'Core 版本')} value={detail.coreVersion || '—'} mono />
          <DetailLine label={t('clientDistOps.colDuration', '耗时')} value={fmtDur(detail.durationMs)} />
          <DetailLine label={t('clientDistOps.colVersion', '版本')} value={detail.version ? `v${detail.version}` : '—'} />
          <DetailLine label={t('clientDistOps.colBytes', '字节')} value={String(detail.bytes ?? 0)} />
          <DetailLine label={t('clientDistOps.colArtifact', '制品')} value={detail.artifactSha ? `${detail.artifactSha.slice(0, 12)}…` : '—'} mono />
        </div>
      </section>

      <section>
        <h4 className="mb-2 text-xs font-semibold">{t('clientDistOps.requestSection', '请求')}</h4>
        <div className="mb-2 grid grid-cols-[4rem_1fr] gap-2 rounded-lg border p-3 text-xs">
          <span className="font-mono text-muted-foreground">{detail.method || 'GET'}</span>
          <span className="break-all font-mono">{detail.path || '—'}</span>
        </div>
        <HeaderList title={t('clientDistOps.requestHeaders')} rows={detail.requestHeaders} />
      </section>

      <section>
        <h4 className="mb-2 text-xs font-semibold">{t('clientDistOps.responseSection', '响应')}</h4>
        <HeaderList title={t('clientDistOps.responseHeaders')} rows={detail.responseHeaders} />
        <div className="mt-2">
          <BodyBlock title={t('clientDistOps.responseBody', '响应体')} body={detail.responseBody} />
        </div>
        {detail.etag ? (
          <p className="mt-2 text-xs text-muted-foreground">
            ETag: <span className="break-all font-mono">{detail.etag}</span>
          </p>
        ) : null}
      </section>
    </div>
  )
}

function DetailLine({ label, value, mono, breakAll }: { label: string; value: string; mono?: boolean; breakAll?: boolean }) {
  return (
    <div className="min-w-0">
      <div className="text-muted-foreground">{label}</div>
      <div className={`${mono ? 'font-mono' : ''} ${breakAll ? 'break-all' : 'break-words'}`}>{value}</div>
    </div>
  )
}

function BodyBlock({ title, body }: { title: string; body?: string }) {
  return (
    <div>
      <h4 className="mb-2 text-xs font-semibold">{title}</h4>
      {body ? (
        <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-lg border p-3 text-xs font-mono">{body}</pre>
      ) : (
        <p className="rounded-lg border p-3 text-xs text-muted-foreground">—</p>
      )}
    </div>
  )
}

function HeaderList({ title, rows }: { title: string; rows: Record<string, string> }) {
  const entries = Object.entries(rows ?? {})
  return (
    <div>
      <h4 className="mb-2 text-xs font-semibold">{title}</h4>
      {entries.length === 0 ? (
        <p className="rounded-lg border p-3 text-xs text-muted-foreground">—</p>
      ) : (
        <div className="overflow-hidden rounded-lg border">
          {entries.map(([k, v]) => (
            <div key={k} className="grid grid-cols-[11rem_1fr] border-b px-3 py-2 text-xs last:border-b-0">
              <span className="font-mono text-muted-foreground">{k}</span>
              <span className="min-w-0 truncate font-mono">{v}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
