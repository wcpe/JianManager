import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { ScrollText } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { useAgentCallLogs, type AgentCallLogFilter } from '@/api/agentObservability'
import { useAgentTokens } from '@/api/agentTokens'
import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { Badge } from '@jianmanager/ui/components/badge'

const ROLE_PLATFORM_ADMIN = 10
const SENTINEL_ALL = '__all__'

/** Agent 调用流水页（FR-391 / FR-390）。 */
export default function AgentCallLogsPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isAdmin = role === ROLE_PLATFORM_ADMIN

  const [tokenId, setTokenId] = useState('')
  const [action, setAction] = useState('')
  const [client, setClient] = useState('')
  const [success, setSuccess] = useState<string>(SENTINEL_ALL)
  const [page, setPage] = useState(1)

  const filter = useMemo((): AgentCallLogFilter => {
    const f: AgentCallLogFilter = { page, pageSize: 50 }
    const tid = Number(tokenId)
    if (Number.isInteger(tid) && tid > 0) f.tokenId = tid
    if (action.trim()) f.action = action.trim()
    if (client.trim() && client !== SENTINEL_ALL) f.client = client.trim()
    if (success === 'true') f.success = true
    if (success === 'false') f.success = false
    return f
  }, [tokenId, action, client, success, page])

  const { data: tokens } = useAgentTokens({ enabled: isAdmin })
  const { data, isLoading, isError, isFetching } = useAgentCallLogs(filter, { enabled: isAdmin })

  if (!isAdmin) {
    return <p className="text-sm text-muted-foreground">{t('agentCallLogs.forbidden')}</p>
  }

  const items = data?.items ?? []
  const total = data?.total ?? 0
  const pageSize = data?.pageSize ?? 50
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  return (
    <div className="jm-page-stack space-y-4">
      <div className="jm-page-header flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="jm-page-title flex items-center gap-2">
            <ScrollText className="size-5 text-primary" aria-hidden />
            {t('agentCallLogs.title')}
          </h1>
          <p className="jm-page-subtitle">{t('agentCallLogs.subtitle')}</p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" asChild>
            <Link to="/mcp-sessions">{t('agentCallLogs.openSessions')}</Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/agent-tokens">{t('agentCallLogs.openTokens')}</Link>
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={tokenId === '' ? SENTINEL_ALL : tokenId}
          onValueChange={(v) => {
            setTokenId(v === SENTINEL_ALL ? '' : v)
            setPage(1)
          }}
        >
          <SelectTrigger size="sm" className="w-48">
            <SelectValue placeholder={t('agentCallLogs.allTokens')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('agentCallLogs.allTokens')}</SelectItem>
            {tokens?.map((tok) => (
              <SelectItem key={tok.id} value={String(tok.id)}>
                {tok.name} ({tok.tokenPrefix}…)
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Input
          value={action}
          onChange={(e) => {
            setAction(e.target.value)
            setPage(1)
          }}
          placeholder={t('agentCallLogs.actionPlaceholder')}
          className="h-9 w-48"
        />
        <Select
          value={client === '' ? SENTINEL_ALL : client}
          onValueChange={(v) => {
            setClient(v === SENTINEL_ALL ? '' : v)
            setPage(1)
          }}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue placeholder={t('agentCallLogs.allClients')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('agentCallLogs.allClients')}</SelectItem>
            {['mcp', 'jmagent', 'curl', 'unknown'].map((c) => (
              <SelectItem key={c} value={c}>
                {t(`agentCallLogs.clients.${c}`, { defaultValue: c })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={success}
          onValueChange={(v) => {
            setSuccess(v)
            setPage(1)
          }}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue placeholder={t('agentCallLogs.allResults')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('agentCallLogs.allResults')}</SelectItem>
            <SelectItem value="true">{t('agentCallLogs.success')}</SelectItem>
            <SelectItem value="false">{t('agentCallLogs.failed')}</SelectItem>
          </SelectContent>
        </Select>
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            setTokenId('')
            setAction('')
            setClient('')
            setSuccess(SENTINEL_ALL)
            setPage(1)
          }}
        >
          {t('agentCallLogs.clear')}
        </Button>
      </div>

      <p className="text-xs text-muted-foreground">
        {t('agentCallLogs.summary', { loaded: items.length, total })}
        {isFetching ? ` · ${t('common.loading')}` : ''}
      </p>

      {isLoading && <p className="text-sm text-muted-foreground">{t('common.loading')}</p>}
      {isError && <p className="text-sm text-destructive">{t('agentCallLogs.loadFailed')}</p>}

      {!isLoading && !isError && items.length === 0 && (
        <Panel className="p-6 text-center text-sm text-muted-foreground">{t('agentCallLogs.empty')}</Panel>
      )}

      {items.length > 0 && (
        <Panel className="overflow-x-auto p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('agentCallLogs.col.time')}</TableHead>
                <TableHead>{t('agentCallLogs.col.token')}</TableHead>
                <TableHead>{t('agentCallLogs.col.action')}</TableHead>
                <TableHead>{t('agentCallLogs.col.client')}</TableHead>
                <TableHead>{t('agentCallLogs.col.transport')}</TableHead>
                <TableHead>{t('agentCallLogs.col.result')}</TableHead>
                <TableHead>{t('agentCallLogs.col.latency')}</TableHead>
                <TableHead>{t('agentCallLogs.col.ip')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((row) => (
                <TableRow key={row.id}>
                  <TableCell className="whitespace-nowrap text-xs tabular-nums">
                    {row.createdAt ? new Date(row.createdAt).toLocaleString() : '—'}
                  </TableCell>
                  <TableCell className="text-xs">
                    <div className="font-medium">{row.tokenName || `#${row.tokenId}`}</div>
                  </TableCell>
                  <TableCell>
                    {(() => {
                      const label = t(`audit.actions.${row.action}`, { defaultValue: row.action })
                      return label === row.action ? (
                        <code className="font-mono text-[11px]">{row.action}</code>
                      ) : (
                        <div className="min-w-0">
                          <div className="truncate text-xs">{label}</div>
                          <code className="font-mono text-[10px] text-muted-foreground">{row.action}</code>
                        </div>
                      )
                    })()}
                    {(row.targetType || row.targetId) && (
                      <div className="text-[10px] text-muted-foreground">
                        {row.targetType}
                        {row.targetId ? `#${row.targetId}` : ''}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="text-xs">
                    {t(`agentCallLogs.clients.${row.client}`, { defaultValue: row.client })}
                  </TableCell>
                  <TableCell className="text-xs">
                    {row.transport
                      ? t(`agentCallLogs.transports.${row.transport}`, { defaultValue: row.transport })
                      : '—'}
                  </TableCell>
                  <TableCell>
                    <Badge variant={row.success ? 'default' : 'destructive'} className="text-[10px]">
                      {row.success ? t('agentCallLogs.success') : t('agentCallLogs.failed')}
                    </Badge>
                    {!row.success && row.error && (
                      <div className="mt-0.5 max-w-[12rem] truncate text-[10px] text-muted-foreground" title={row.error}>
                        {row.error}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="tabular-nums text-xs">
                    {row.latencyMs != null ? `${row.latencyMs} ms` : '—'}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{row.ip || '—'}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}

      {totalPages > 1 && (
        <div className="flex items-center justify-end gap-2">
          <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>
            {t('agentCallLogs.prev')}
          </Button>
          <span className="text-xs text-muted-foreground">
            {page} / {totalPages}
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={page >= totalPages}
            onClick={() => setPage((p) => p + 1)}
          >
            {t('agentCallLogs.next')}
          </Button>
        </div>
      )}
    </div>
  )
}
