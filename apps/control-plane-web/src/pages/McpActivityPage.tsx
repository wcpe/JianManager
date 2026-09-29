import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Link } from 'react-router'
import { Cable, RefreshCw } from 'lucide-react'
import { useAuthStore } from '@/stores/auth'
import { useMcpActivity, mcpBaseUrl } from '@/api/agentObservability'
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

/**
 * 统计窗口档位：取值会原样作为 `window` 查询参数发给后端，因此必须能被
 * `internal/controlplane/mcp/handler.go` 的 `ListActivity` 按 Go duration 解析，
 * 并落在闭区间 1h~168h（同文件的 minActivityWindow / maxActivityWindow），否则该请求被 400 拒绝。
 * Go duration 没有「天」单位，所以「7 天」只能写 `168h`——写成 `7d` 会被后端判为非法。
 * 档位与后端可解析性的契约由 McpActivityPage.dom.test.tsx 的形态/区间守卫测试兜底。
 */
// eslint-disable-next-line react-refresh/only-export-components -- 档位需被单测逐项枚举，导出为本次改动的必要部分
export const WINDOW_PRESETS = ['24h', '168h'] as const
type WindowPreset = (typeof WINDOW_PRESETS)[number]
const DEFAULT_WINDOW: WindowPreset = '24h'

/** 客户端标识 → 调用次数 映射转成「客户端 ×次数」列表，按次数降序。 */
function formatClients(clients: Record<string, number> | undefined): string {
  const entries = Object.entries(clients ?? {})
  if (entries.length === 0) return '—'
  return entries
    .sort((a, b) => b[1] - a[1])
    .map(([client, count]) => `${client} ×${count}`)
    .join('，')
}

/**
 * MCP 活动运维页（FR-391 / ADR-096）：按 Token 聚合的调用活动、MCP URL 复制与吊销入口。
 * 端点去会话化后已无「会话」可观测，故本页只呈现窗口内的活动聚合。
 */
export default function McpActivityPage() {
  const { t } = useTranslation()
  const role = useAuthStore((s) => s.role)
  const isAdmin = role === ROLE_PLATFORM_ADMIN
  const [windowValue, setWindowValue] = useState<WindowPreset>(DEFAULT_WINDOW)

  const { data, isLoading, isError, refetch, isFetching } = useMcpActivity(windowValue, {
    enabled: isAdmin,
  })

  if (!isAdmin) {
    return <p className="text-sm text-muted-foreground">{t('mcpActivity.forbidden')}</p>
  }

  const items = data?.items ?? []
  const base = mcpBaseUrl()

  const onCopyUrl = async () => {
    const ok = await copyToClipboard(base)
    if (ok) toast.success(t('mcpActivity.copied'))
    else toast.error(t('mcpActivity.copyFailed'))
  }

  return (
    <div className="jm-page-stack space-y-4">
      <div className="jm-page-header flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="jm-page-title flex items-center gap-2">
            <Cable className="size-5 text-primary" aria-hidden />
            {t('mcpActivity.title')}
          </h1>
          <p className="jm-page-subtitle">{t('mcpActivity.subtitle')}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div
            className="flex items-center gap-1 rounded-md border p-0.5"
            role="group"
            aria-label={t('mcpActivity.window')}
          >
            {WINDOW_PRESETS.map((w) => (
              <Button
                key={w}
                variant={windowValue === w ? 'secondary' : 'ghost'}
                size="sm"
                aria-pressed={windowValue === w}
                onClick={() => setWindowValue(w)}
              >
                {t(`mcpActivity.window_${w}`)}
              </Button>
            ))}
          </div>
          <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isFetching}>
            <RefreshCw className={`size-3.5 ${isFetching ? 'animate-spin' : ''}`} />
            {t('mcpActivity.refresh')}
          </Button>
          <Button variant="outline" size="sm" onClick={() => void onCopyUrl()}>
            {t('mcpActivity.copyUrl')}
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/agent-call-logs">{t('mcpActivity.openLogs')}</Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link to="/agent-tokens">{t('mcpActivity.revokeToken')}</Link>
          </Button>
        </div>
      </div>

      <Panel className="space-y-2 p-3 text-sm text-muted-foreground">
        <div>
          <span className="font-medium text-foreground">{t('mcpActivity.endpoint')}: </span>
          <code className="break-all font-mono text-xs">{base}</code>
        </div>
        <p>{t('mcpActivity.endpointHint')}</p>
        <p className="text-xs">{t('mcpActivity.endpointHintSse')}</p>
        <p className="text-xs">{t('mcpActivity.authNote')}</p>
        <p className="text-xs">{t('mcpActivity.protocolVersion')}</p>
        {data?.generatedAt && (
          <p className="text-xs">
            {t('mcpActivity.generatedAt', { time: new Date(data.generatedAt).toLocaleString() })}
          </p>
        )}
      </Panel>

      {isLoading && <p className="text-sm text-muted-foreground">{t('common.loading')}</p>}
      {isError && <p className="text-sm text-destructive">{t('mcpActivity.loadFailed')}</p>}

      {!isLoading && !isError && items.length === 0 && (
        <Panel className="p-6 text-center text-sm text-muted-foreground">{t('mcpActivity.empty')}</Panel>
      )}

      {items.length > 0 && (
        <Panel className="overflow-x-auto p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('mcpActivity.col.token')}</TableHead>
                <TableHead>{t('mcpActivity.col.client')}</TableHead>
                <TableHead>{t('mcpActivity.col.lastActivity')}</TableHead>
                <TableHead>{t('mcpActivity.col.lastAction')}</TableHead>
                <TableHead className="text-right">{t('mcpActivity.col.calls')}</TableHead>
                <TableHead className="text-right">{t('mcpActivity.col.failures')}</TableHead>
                <TableHead>{t('mcpActivity.col.ip')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => (
                <TableRow key={item.tokenId}>
                  <TableCell>
                    <div className="font-medium">{item.tokenName || `Token #${item.tokenId}`}</div>
                    <code className="font-mono text-[11px] text-muted-foreground">
                      {item.tokenPrefix}…
                    </code>
                  </TableCell>
                  <TableCell
                    className="max-w-[12rem] truncate text-xs"
                    title={formatClients(item.clients)}
                  >
                    {formatClients(item.clients)}
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs tabular-nums">
                    {item.lastActivityAt ? new Date(item.lastActivityAt).toLocaleString() : '—'}
                  </TableCell>
                  <TableCell
                    className="max-w-[10rem] truncate font-mono text-xs"
                    title={item.lastAction}
                  >
                    {item.lastAction || '—'}
                  </TableCell>
                  <TableCell className="text-right text-xs tabular-nums">{item.callCount}</TableCell>
                  <TableCell className="text-right text-xs tabular-nums">
                    {item.failureCount}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {item.clientIPs?.length ? item.clientIPs.join('，') : '—'}
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
