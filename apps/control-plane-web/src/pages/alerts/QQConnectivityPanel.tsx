import { useTranslation } from 'react-i18next'
import { Activity } from 'lucide-react'
import {
  QQ_GATEWAY_POLL_MS,
  useQQDiscoveredGroups,
  useQQGatewayStatus,
} from '@/api/alerts'
import { Panel } from '@jianmanager/ui/components/panel'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { formatRelativeTime } from '@/lib/shared/relative-time'
import {
  qqGatewayFatalHintKey,
  qqGatewayLevel,
  qqGatewayStatusKey,
  shortGroupOpenid,
} from '@/lib/alerts/alert-helpers'

/** 折叠区里最多列几个群——超过部分只报总数（后端默认一页 20 条，全展开会把对话框撞满）。 */
const GROUP_PREVIEW_LIMIT = 8

/**
 * QQ 接入状态只读观测区。
 *
 * 为什么需要它：`qq_discovered_groups` 在落库、网关在连、`GET /qq/groups` 有数据，但控制台上
 * **什么也看不见**。一旦网关掉线（`1<<25` 权限失效 4014、会话失效 4006），运维只能翻日志。
 * 本组件把两个现成端点接上，让「网关到底连没连、机器人进了哪些群」在通道配置页一眼可查。
 *
 * 为什么是**只读**：群主动消息被平台拒（40034105，见 spec §6），恢复 `targetType=group`
 * 选项等于给运维一个用不了的功能。所以这里只展示状态，不提供任何群选择入口、不支持手工填
 * `group_openid`——观测是纯收益，建群通道是纯陷阱。
 *
 * 为什么不用 toast 报错误：状态区在对话框里，失败只说明「这次查询没成」，与用户正在做的事
 * （填凭证/保存通道）无关；弹 toast 会盖住真正需要他处理的表单错误。区块内联错误态即可。
 */
export function QQConnectivityPanel() {
  const { t } = useTranslation()
  // 两个查询都不抛错到全局：错误在各自区块内联展示（见上方注释）。轮询 30s，卸载即停。
  const gateway = useQQGatewayStatus(QQ_GATEWAY_POLL_MS)
  const groups = useQQDiscoveredGroups(true, QQ_GATEWAY_POLL_MS)

  return (
    <Panel
      title={t('alerts.qqConnectivity')}
      icon={<Activity className="size-3.5" />}
      bodyClassName="space-y-3 p-3"
    >
      <QQGatewayStatusBlock
        isLoading={gateway.isLoading}
        isError={gateway.isError}
        status={gateway.data?.status}
        appId={gateway.data?.appId}
        lastError={gateway.data?.lastError}
        lastEventAt={gateway.data?.lastEventAt}
        onRetry={() => void gateway.refetch()}
      />
      <DiscoveredGroupsBlock
        isLoading={groups.isLoading}
        isError={groups.isError}
        items={groups.data?.items ?? []}
        total={groups.data?.total ?? 0}
      />
    </Panel>
  )
}

/** 网关连接状态区块：状态徽章 + 最近事件时间 + lastError 原文与可操作提示。 */
function QQGatewayStatusBlock({
  isLoading,
  isError,
  status,
  appId,
  lastError,
  lastEventAt,
  onRetry,
}: {
  isLoading: boolean
  isError: boolean
  /** 后端状态串（connected/connecting/disconnected/error）；未加载时为 undefined。 */
  status?: string
  /** 默认机器人的 appId（未配置 qq 通道时为空串）。 */
  appId?: string
  /** 后端 lastError 原文（可能为空）。 */
  lastError?: string
  /** 最近一次收到网关事件的时间（从未收到时为空）。 */
  lastEventAt?: string | null
  onRetry: () => void
}) {
  const { t } = useTranslation()

  if (isLoading) {
    return (
      <div className="space-y-1.5" data-testid="qq-gateway-loading">
        <p className="text-sm font-medium">{t('alerts.qqGatewayTitle')}</p>
        <Skeleton className="h-4 w-40" />
      </div>
    )
  }

  if (isError) {
    return (
      <div className="space-y-1">
        <p className="text-sm font-medium">{t('alerts.qqGatewayTitle')}</p>
        {/* role=alert：查询失败是需要读屏用户立即知道的状态，且与下方群列表失败可以各自独立报。 */}
        <p className="text-xs text-muted-foreground" role="alert" data-testid="qq-gateway-error">
          {t('alerts.qqGatewayLoadFailed')}
        </p>
        <button type="button" className="text-xs underline underline-offset-2" onClick={onRetry}>
          {t('alerts.qqGatewayRetry')}
        </button>
      </div>
    )
  }

  const statusKey = qqGatewayStatusKey(status)
  const hintKey = qqGatewayFatalHintKey(lastError)
  const errorText = (lastError ?? '').trim()
  const seen = formatRelativeTime(lastEventAt)

  return (
    <div className="space-y-1">
      <p className="text-sm font-medium">{t('alerts.qqGatewayTitle')}</p>
      <div className="flex flex-wrap items-center gap-2" data-testid="qq-gateway-status">
        <StatusBadge
          level={qqGatewayLevel(status)}
          label={t(`alerts.qqGatewayStatus_${statusKey}`)}
          // connected 是稳态，不脉冲；connecting 才在「进行中」。
          pulse={statusKey === 'connecting'}
        />
        {appId ? <span className="font-mono text-xs text-muted-foreground">{appId}</span> : null}
        {seen ? (
          <span className="text-xs text-muted-foreground">
            {t('alerts.qqGatewayLastEvent', { time: seen })}
          </span>
        ) : null}
      </div>
      {/* 后端文案是中文（closeErrorText / ResolveCredentials），故逐字展示原文；
          英文界面另给本地化提示（见 qqGatewayFatalHintKey）。 */}
      {errorText ? (
        <p className="text-xs text-status-danger break-words" data-testid="qq-gateway-last-error">
          {errorText}
        </p>
      ) : null}
      {hintKey ? (
        <p className="text-xs text-muted-foreground" data-testid="qq-gateway-hint">
          {t(hintKey)}
        </p>
      ) : null}
      {/* disconnected 且无错误 = 尚未配置启用的 qq 通道（后端 loop 在凭证解析失败时也置
          disconnected 并写原因，故「无错误串」正是「还没配」的形态，不是异常）。 */}
      {statusKey === 'disconnected' && !errorText ? (
        <p className="text-xs text-muted-foreground">{t('alerts.qqGatewayIdleHint')}</p>
      ) : null}
    </div>
  )
}

/** 已发现群折叠区：只读列表 + 明确「这些群当前收不到告警」的免责文案。 */
function DiscoveredGroupsBlock({
  isLoading,
  isError,
  items,
  total,
}: {
  isLoading: boolean
  isError: boolean
  items: { id: number; groupOpenid: string; firstSeenAt: string; lastSeenAt: string }[]
  /** 后端信封 total（可能大于本页条数）。 */
  total: number
}) {
  const { t } = useTranslation()

  return (
    <div className="space-y-1 border-t pt-3">
      <p className="text-sm font-medium">{t('alerts.qqGroupsTitle', { count: total })}</p>
      {/* 免责文案常驻（不因 0 条而隐藏）：它解释的是「这里为什么不能拿来建通道」，
          恰恰在运维看到一群可用群、想选一个时最需要。 */}
      <p className="text-xs text-muted-foreground">{t('alerts.qqGroupsReadonlyHint')}</p>

      {isLoading ? (
        <Skeleton className="h-4 w-32" data-testid="qq-groups-loading" />
      ) : isError ? (
        <p className="text-xs text-muted-foreground" role="alert" data-testid="qq-groups-error">
          {t('alerts.qqGroupsLoadFailed')}
        </p>
      ) : items.length === 0 ? (
        <p className="text-xs text-muted-foreground" data-testid="qq-groups-empty">
          {t('alerts.qqGroupsEmpty')}
        </p>
      ) : (
        // 默认收起：列表是观测附属信息，不应该把「填凭证 / 保存」这些主动作顶出视口
        // （对话框正文靠 ScrollableDialogBody 内部滚动，展开后才滚）。
        <details className="rounded-md border bg-muted/20 p-2">
          <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
            {t('alerts.qqGroupsToggle', { count: items.length })}
          </summary>
          <ul className="mt-2 space-y-1" data-testid="qq-groups-list">
            {items.slice(0, GROUP_PREVIEW_LIMIT).map((g) => (
              <li key={g.id} className="flex flex-wrap items-center gap-2 text-xs">
                <span className="font-mono" title={g.groupOpenid}>
                  {shortGroupOpenid(g.groupOpenid)}
                </span>
                <span className="text-muted-foreground">
                  {t('alerts.qqGroupSeen', {
                    first: formatRelativeTime(g.firstSeenAt),
                    last: formatRelativeTime(g.lastSeenAt),
                  })}
                </span>
              </li>
            ))}
          </ul>
          {total > GROUP_PREVIEW_LIMIT ? (
            <p className="mt-1 text-xs text-muted-foreground">
              {t('alerts.qqGroupsMore', { shown: GROUP_PREVIEW_LIMIT, total })}
            </p>
          ) : null}
        </details>
      )}
    </div>
  )
}
