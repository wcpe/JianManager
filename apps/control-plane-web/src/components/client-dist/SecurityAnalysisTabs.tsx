import { useTranslation } from 'react-i18next'
import { useClientDistIpAnalysis, useClientDistPlayerAnalysis } from '@/api/clientDistSecurity'
import { Badge } from '@jianmanager/ui/components/badge'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { EmptyState, SECURITY_EMPTY as EMPTY, fmtBytes, fmtTime } from './security-shared'

/**
 * 安全侧「IP 剖析 / 玩家名剖析」两个只读聚合 Tab（FR-430 / ADR-088）。
 * 由旧 `ProtectionCenterPage.tsx` 内联实现原样迁出，行为不变。
 */

export function IpAnalysisTab() {
  const { t } = useTranslation()
  const { data, isError, isLoading } = useClientDistIpAnalysis({ limit: 200 })
  const rows = data ?? []
  return (
    <Panel title={t('clientDistOps.ip.title')}>
      {isError ? (
        <EmptyState text={t('clientDistOps.ip.error')} />
      ) : rows.length === 0 ? (
        <EmptyState text={isLoading ? t('common.loading') : t('clientDistOps.ip.empty')} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('clientDistOps.ip.colIp')}</TableHead>
              <TableHead>{t('clientDistOps.ip.colReqReject')}</TableHead>
              <TableHead>{t('clientDistOps.ip.colInvalidKey')}</TableHead>
              <TableHead>{t('clientDistOps.ip.colDownload')}</TableHead>
              <TableHead>{t('clientDistOps.ip.colKeyChannel')}</TableHead>
              <TableHead>{t('clientDistOps.ip.colRisk')}</TableHead>
              <TableHead>{t('clientDistOps.ip.colBlocked')}</TableHead>
              <TableHead>{t('clientDistOps.ip.colLastSeen')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.ip}>
                <TableCell className="font-medium">{row.ip}</TableCell>
                <TableCell>{row.requestCount} / {row.rejectCount}</TableCell>
                <TableCell>{row.invalidKeyCount} / {row.notFoundCount} / {row.rangeCount}</TableCell>
                <TableCell>{fmtBytes(row.downloadBytes)}</TableCell>
                <TableCell>{row.keyCount} / {row.channelCount}</TableCell>
                <TableCell>{row.riskScore}</TableCell>
                <TableCell><Badge variant={row.blocked ? 'destructive' : 'secondary'}>{row.blocked ? t('clientDistOps.ip.blocked') : t('clientDistOps.ip.unblocked')}</Badge></TableCell>
                <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(row.lastSeen)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}

export function PlayerAnalysisTab() {
  const { t } = useTranslation()
  const { data, isError, isLoading } = useClientDistPlayerAnalysis({ limit: 200 })
  const rows = data ?? []
  return (
    <Panel title={t('clientDistOps.players.title')}>
      {isError ? (
        <EmptyState text={t('clientDistOps.players.error')} />
      ) : rows.length === 0 ? (
        <EmptyState text={isLoading ? t('common.loading') : t('clientDistOps.players.empty')} />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('clientDistOps.players.colPlayer')}</TableHead>
              <TableHead>{t('clientDistOps.players.colInstallMachine')}</TableHead>
              <TableHead>{t('clientDistOps.players.colIpKeyChannel')}</TableHead>
              <TableHead>{t('clientDistOps.players.colDownload')}</TableHead>
              <TableHead>{t('clientDistOps.players.colAbnormal')}</TableHead>
              <TableHead>{t('clientDistOps.players.colRisk')}</TableHead>
              <TableHead>{t('clientDistOps.players.colLastSeen')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => (
              <TableRow key={row.playerName}>
                <TableCell className="font-medium">{row.playerName || EMPTY}</TableCell>
                <TableCell>{row.installCount} / {row.machineCount}</TableCell>
                <TableCell>{row.ipCount} / {row.keyCount} / {row.channelCount}</TableCell>
                <TableCell>{fmtBytes(row.downloadBytes)}</TableCell>
                <TableCell>{row.abnormalRequests}</TableCell>
                <TableCell>{row.riskScore}</TableCell>
                <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(row.lastSeen)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  )
}
