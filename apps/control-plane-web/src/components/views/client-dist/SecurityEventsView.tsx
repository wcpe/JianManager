import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import type { ClientDistSecurityEvent } from '@/lib/client-dist/client-dist-security-contracts'
import type { ClientDistQuery } from '@/lib/client-dist/client-dist-query'
import { EmptyState } from '@/components/views/client-dist/security-format'

/**
 * 安全侧「异常请求分析」Tab 的展示层（FR-430 / ADR-088）。
 * 取数（`useClientDistSecurityEvents`）、查询读写（`useSecurityQuery`）与行组件
 * （含处置动作 mutation）均由应用侧容器提供。
 */
export function SecurityEventsView({
  events,
  truncated,
  isLoading,
  isError,
  query,
  onQueryChange,
  riskRule,
  onRiskRuleChange,
  renderRow,
}: {
  events: ClientDistSecurityEvent[]
  /** 命中数已达服务端上限：列表可能被截断，需明确告知（固定 limit 无翻页器）。 */
  truncated?: boolean
  isLoading: boolean
  isError: boolean
  query: ClientDistQuery
  onQueryChange: (patch: Partial<Record<string, string | null>>) => void
  riskRule: string
  onRiskRuleChange: (value: string) => void
  /** 事件行（应用侧容器渲染，内含处置动作 mutation）。 */
  renderRow: (event: ClientDistSecurityEvent) => ReactNode
}) {
  const { t } = useTranslation()
  return (
    <Panel
      title={t('clientDistOps.events.title')}
      actions={
        <div className="flex flex-wrap gap-2">
          <Input className="w-36" placeholder={t('clientDistOps.events.phChannel')} value={query.channelId ?? ''} onChange={(e) => onQueryChange({ channelId: e.target.value || null })} />
          <Input className="w-36" placeholder={t('clientDistOps.events.phIp')} value={query.ip ?? ''} onChange={(e) => onQueryChange({ ip: e.target.value || null })} />
          <Input className="w-40" placeholder={t('clientDistOps.events.phErrCode')} value={query.errCode ?? ''} onChange={(e) => onQueryChange({ errCode: e.target.value || null })} />
          <Input className="w-40" placeholder={t('clientDistOps.events.phRiskRule')} value={riskRule} onChange={(e) => onRiskRuleChange(e.target.value)} />
        </div>
      }
    >
      {isError ? (
        <EmptyState text={t('clientDistOps.events.error')} />
      ) : events.length === 0 ? (
        <EmptyState text={isLoading ? t('common.loading') : t('clientDistOps.events.empty')} />
      ) : (
        <>
          {/* 固定 limit=200 且无翻页器：命中触顶时列表是被截断的，不能让人当成「就这些」。 */}
          {truncated && <p className="mb-2 text-xs text-muted-foreground">{t('search.truncated')}</p>}
          <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('clientDistOps.events.colTime')}</TableHead>
              <TableHead>{t('clientDistOps.events.colLevel')}</TableHead>
              <TableHead>{t('clientDistOps.events.colIpPlayer')}</TableHead>
              <TableHead>{t('clientDistOps.events.colChannelKey')}</TableHead>
              <TableHead>{t('clientDistOps.events.colEndpoint')}</TableHead>
              <TableHead>{t('clientDistOps.events.colRuleErrCode')}</TableHead>
              <TableHead>{t('clientDistOps.events.colAction')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {events.map((event) => renderRow(event))}
          </TableBody>
        </Table>
        </>
      )}
    </Panel>
  )
}
