import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ShieldAlert } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import type { ClientChannelSecuritySummary } from '@jianmanager/ui/lib/client-dist-security-contracts'

export interface ChannelSecuritySummaryBarProps {
  /** 安全摘要数据（容器经 useClientChannelSecuritySummary 取数）。 */
  summary?: ClientChannelSecuritySummary
  /** 取数失败（展示「暂不可用」文案）。 */
  isError?: boolean
  /** 取数中（展示加载文案）。 */
  isLoading?: boolean
  /** 安全中心链接地址（容器按当前查询构造）。 */
  securityHref: string
  /** 链接渲染插槽：应用侧注入路由 Link；缺省用原生 `<a>`。 */
  renderLink?: (args: { href: string; children: ReactNode }) => ReactNode
}
/** 风险等级 → Badge 变体（FR-358）。 */
export function riskBadgeVariant(level?: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (level === 'critical' || level === 'high') return 'destructive'
  if (level === 'warn') return 'default'
  return 'secondary'
}

/** 频道工作台安全摘要条（FR-358）：近窗风险与封禁/受限计数，链到安全中心。 */
export function ChannelSecuritySummaryBar({
  summary,
  isError = false,
  isLoading = false,
  securityHref,
  renderLink,
}: ChannelSecuritySummaryBarProps) {
  const { t } = useTranslation()

  return (
    <div
      data-testid="channel-security-summary"
      className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border/70 bg-muted/20 px-3 py-2"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-3 text-sm">
        <div className="flex items-center gap-1.5 font-medium">
          <ShieldAlert className="size-4 text-muted-foreground" />
          <span>{t('clientChannels.securitySummary', '安全摘要')}</span>
        </div>
        {isError ? (
          <span className="text-xs text-muted-foreground">{t('clientChannels.securitySummaryUnavailable', '安全摘要暂不可用')}</span>
        ) : isLoading || !summary ? (
          <span className="text-xs text-muted-foreground">{t('common.loading', '加载中…')}</span>
        ) : (
          <>
            <Badge variant={riskBadgeVariant(summary.riskLevel)}>{summary.riskLevel || 'info'}</Badge>
            <span className="text-xs text-muted-foreground">
              {t('clientChannels.securityAbnormal', '近窗异常')} {summary.abnormalRequests}
            </span>
            <span className="text-xs text-muted-foreground">
              {t('clientChannels.securityBlockedIp', '封禁 IP')} {summary.blockedIpCount}
            </span>
            <span className="text-xs text-muted-foreground">
              {t('clientChannels.securityRestrictedKey', '受限 Key')} {summary.restrictedKeyCount}
            </span>
            {summary.protectionMode ? (
              <span className="text-xs text-muted-foreground">
                {t('clientChannels.securityProtectionMode', '防护')} {summary.protectionMode}
              </span>
            ) : null}
            {summary.windowMinutes ? (
              <span className="text-xs text-muted-foreground">
                {t('clientChannels.securityWindow', '窗口')} {summary.windowMinutes}m
              </span>
            ) : null}
          </>
        )}
      </div>
      <Button asChild size="sm" variant="outline">
        {renderLink
          ? renderLink({ href: securityHref, children: t('clientChannels.openSecurityCenter', '打开安全中心') })
          : <a href={securityHref}>{t('clientChannels.openSecurityCenter', '打开安全中心')}</a>}
      </Button>
    </div>
  )
}
