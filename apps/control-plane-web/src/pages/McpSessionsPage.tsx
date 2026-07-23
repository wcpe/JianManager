import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Link } from 'react-router'
import { Cable, RefreshCw } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { useMcpSessions, useKickMcpSession, mcpBaseUrl } from '@/api/agentObservability'
import { copyToClipboard } from '@/lib/clipboard'
import { Panel } from '@jianmanager/ui/components/panel'
import { Button } from '@jianmanager/ui/components/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'

const ROLE_PLATFORM_ADMIN = 10

type ErrResp = { response?: { data?: { message?: string }; status?: number } }
const errMsg = (e: unknown, fallback: string) => (e as ErrResp)?.response?.data?.message || fallback

/** MCP 会话运维页（FR-391 / FR-389）：列表、踢线、超时说明与 MCP URL 复制。 */
export default function McpSessionsPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isAdmin = role === ROLE_PLATFORM_ADMIN

  const { data, isLoading, isError, refetch, isFetching } = useMcpSessions({ enabled: isAdmin })
  const kick = useKickMcpSession()

  if (!isAdmin) {
    return <p className="text-sm text-muted-foreground">{t('mcpSessions.forbidden')}</p>
  }

  const sessions = data?.sessions ?? []
  const cfg = data?.config
  const base = mcpBaseUrl()

  const onCopyUrl = async () => {
    const ok = await copyToClipboard(base)
    if (ok) toast.success(t('mcpSessions.copied'))
    else toast.error(t('mcpSessions.copyFailed'))
  }

  const onKick = (id: string) => {
    kick.mutate(id, {
      onSuccess: () => toast.success(t('mcpSessions.kickSuccess')),
      onError: (e) => toast.error(errMsg(e, t('mcpSessions.kickFailed'))),
    })
  }

  return (
    <div className="jm-page-stack space-y-4">
      <div className="jm-page-header flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="jm-page-title flex items-center gap-2">
            <Cable className="size-5 text-primary" aria-hidden />
            {t('mcpSessions.title')}
          </h1>
          <p className="jm-page-subtitle">{t('mcpSessions.subtitle')}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
            <RefreshCw className={`size-3.5 ${isFetching ? 'animate-spin' : ''}`} />
            {t('mcpSessions.refresh')}
          </Button>
          <Button variant="outline" size="sm" onClick={() => void onCopyUrl()}>
            {t('mcpSessions.copyUrl')}
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/agent-call-logs">{t('mcpSessions.openLogs')}</Link>
          </Button>
        </div>
      </div>

      <Panel className="space-y-2 p-3 text-sm text-muted-foreground">
        <div>
          <span className="font-medium text-foreground">{t('mcpSessions.endpoint')}: </span>
          <code className="break-all font-mono text-xs">{base}</code>
        </div>
        <p>{t('mcpSessions.endpointHint')}</p>
        <p className="text-xs">{t('mcpSessions.endpointHintSse')}</p>
        <p className="text-xs">{t('mcpSessions.authNote')}</p>
        <p className="text-xs">{t('mcpSessions.protocolVersion')}</p>
        {cfg && (
          <p className="text-xs">
            {t('mcpSessions.timeouts', {
              idle: cfg.idleTimeout ?? '—',
              absolute: cfg.absoluteTimeout ?? '—',
              global: cfg.maxGlobalSessions ?? '—',
              perToken: cfg.maxSessionsPerToken ?? '—',
            })}
          </p>
        )}
      </Panel>

      {isLoading && <p className="text-sm text-muted-foreground">{t('common.loading')}</p>}
      {isError && <p className="text-sm text-destructive">{t('mcpSessions.loadFailed')}</p>}

      {!isLoading && !isError && sessions.length === 0 && (
        <Panel className="p-6 text-center text-sm text-muted-foreground">{t('mcpSessions.empty')}</Panel>
      )}

      {sessions.length > 0 && (
        <Panel className="overflow-x-auto p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('mcpSessions.col.token')}</TableHead>
                <TableHead>{t('mcpSessions.col.transport')}</TableHead>
                <TableHead>{t('mcpSessions.col.ip')}</TableHead>
                <TableHead>{t('mcpSessions.col.connected')}</TableHead>
                <TableHead>{t('mcpSessions.col.lastActivity')}</TableHead>
                <TableHead>{t('mcpSessions.col.lastTool')}</TableHead>
                <TableHead className="text-right">{t('mcpSessions.col.actions')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {sessions.map((s) => (
                <TableRow key={s.sessionId}>
                  <TableCell>
                    <div className="font-medium">{s.tokenName || `Token #${s.tokenId}`}</div>
                    <code className="font-mono text-[11px] text-muted-foreground">{s.tokenPrefix}…</code>
                  </TableCell>
                  <TableCell className="text-xs">
                    {s.transport
                      ? t(`agentCallLogs.transports.${s.transport}`, { defaultValue: s.transport })
                      : '—'}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{s.clientIP || '—'}</TableCell>
                  <TableCell className="whitespace-nowrap text-xs tabular-nums">
                    {s.connectedAt ? new Date(s.connectedAt).toLocaleString() : '—'}
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs tabular-nums">
                    {s.lastActivityAt ? new Date(s.lastActivityAt).toLocaleString() : '—'}
                  </TableCell>
                  <TableCell className="max-w-[10rem] truncate font-mono text-xs" title={s.lastTool}>
                    {s.lastTool || '—'}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="outline"
                      size="sm"
                      className="text-destructive"
                      disabled={kick.isPending}
                      onClick={() => onKick(s.sessionId)}
                    >
                      {t('mcpSessions.kick')}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}
    </div>
  )
}
