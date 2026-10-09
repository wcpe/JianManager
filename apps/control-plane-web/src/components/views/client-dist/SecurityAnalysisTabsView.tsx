import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import type { ClientDistIpAnalysis, ClientDistPlayerAnalysis } from '@/lib/client-dist/client-dist-security-contracts'
import { EmptyState, SECURITY_EMPTY as EMPTY, fmtBytes, fmtTime } from '@/components/views/client-dist/security-format'

/**
 * 安全侧「IP 剖析 / 玩家名剖析」两个只读聚合 Tab（FR-430 / ADR-088）。
 * 由旧 `ProtectionCenterPage.tsx` 内联实现原样迁出，行为不变。
 *
 * 受控视图（ADR-097 a 范式）：不取数、不碰路由查询、不弹 toast——
 * 聚合结果与加载/错误态经 props 注入（应用侧容器调 `useClientDistIpAnalysis` /
 * `useClientDistPlayerAnalysis` 并提供容器与回调），本组件只保留展示逻辑：
 * 表格结构、列顺序、格式化与空/加载/错误三态的判定优先级。
 */

/** IP 剖析表的注入契约（行数据来源：`useClientDistIpAnalysis({ limit: 200 })`）。 */
export interface IpAnalysisTabViewProps {
  /** 聚合行（空数组即空态）。 */
  rows: ClientDistIpAnalysis[]
  /** 取数中；仅在无行时展示「加载中」文案。 */
  isLoading: boolean
  /** 取数失败；判定优先于空态。 */
  isError: boolean
}

export function IpAnalysisTabView({ rows, isLoading, isError }: IpAnalysisTabViewProps) {
  const { t } = useTranslation()
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

/** 玩家名剖析表的注入契约（行数据来源：`useClientDistPlayerAnalysis({ limit: 200 })`）。 */
export interface PlayerAnalysisTabViewProps {
  /** 聚合行（空数组即空态）。 */
  rows: ClientDistPlayerAnalysis[]
  /** 取数中；仅在无行时展示「加载中」文案。 */
  isLoading: boolean
  /** 取数失败；判定优先于空态。 */
  isError: boolean
}

export function PlayerAnalysisTabView({ rows, isLoading, isError }: PlayerAnalysisTabViewProps) {
  const { t } = useTranslation()
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
