import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Boxes, ChevronDown, ChevronRight, FileJson, Loader2 } from 'lucide-react'

import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { Panel } from '@jianmanager/ui/components/panel'
import { RefreshButton } from '@jianmanager/ui/components/refresh-button'
import { cn } from '@jianmanager/ui'
import type { BusinessStatus, DomainStatus } from '@/api/businessStatus'
import type { BusinessManifest } from '@/lib/console/business'
import { formatRelativeTime } from '@/lib/shared/relative-time'

/**
 * 业务插件域状态面板（FR-534，见 docs/specs/business-plugin-panel/spec.md §4.2）。
 *
 * **只渲染不解释**（spec §2 的核心原则）：把探针上报的域清单原样铺开——域名、可用与否、
 * 调用/失败计数、最近调用时间、manifest 指纹——不做业务语义归类，也不解析 manifest 内容。
 * 「查看 manifest」展开的是探针透传的 manifest 原文（本组件只做 `JSON.stringify` 排版）。
 *
 * 受控视图（ADR-102 a 范式）：不取数、不发请求——域状态与 manifest 由外壳注入，
 * 「刷新」经 `onRefresh` 上报；抽屉展开态是纯 UI 状态，留在这里。
 *
 * 与 manifest 动作台（`components/views/instances/BusinessSegment.tsx`）的分工：
 * 本面板回答「有哪些域、健不健康」（只读概览），动作台回答「能做什么动作」（下发操作）。
 * 两者在业务页签上下并存，互不替代。
 */
export interface BusinessDomainStatusProps {
  /** 后端返回的域状态（未加载完时为 undefined）。 */
  status?: BusinessStatus
  /**
   * 实例的业务能力清单（探针透传的 manifest）。
   * 只用于「查看 manifest」抽屉展示原文——本组件不从中推断任何业务含义。
   */
  manifest?: BusinessManifest
  /** 首次加载中。 */
  loading?: boolean
  /** 刷新中（与首次加载分开：刷新时保留旧数据渲染，避免闪空白）。 */
  refreshing?: boolean
  /** 查询失败（权限 / 实例不存在这类硬失败；探针不可达走 `status.stale`）。 */
  error?: boolean
  /** 刷新域状态。 */
  onRefresh: () => void
  /** 外壳追加的尺寸约束（如页签内的高度上限）。 */
  className?: string
}

/**
 * manifest 指纹的展示形态：spec §4.2 写的是 `a3f2b8c1…` 这种「前八位 + 省略号」。
 * 指纹是探针给的内容摘要，长度不保证恰好 64，故只截断不假设长度；过短的整段显示。
 */
function digestPreview(digest: string): string {
  return digest.length > 8 ? `${digest.slice(0, 8)}…` : digest
}

/**
 * 「查看 manifest」抽屉（spec §4.2）：展开显示该域的 manifest 原文 + 免责声明。
 *
 * 原文取自注入的 manifest（同一份数据也是动作台的能力来源），**不做任何字段解释**；
 * 该域在 manifest 里没有条目时不渲染入口——展示空 JSON 比不展示更容易误导。
 */
function ManifestDrawer({ domain, manifest }: { domain: DomainStatus; manifest?: BusinessManifest }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const declared = manifest?.domains?.[domain.domain]
  if (!declared) return null

  return (
    <div>
      <Button
        type="button"
        variant="ghost"
        size="xs"
        className="text-muted-foreground hover:text-foreground"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {open ? <ChevronDown className="size-3" aria-hidden /> : <ChevronRight className="size-3" aria-hidden />}
        <FileJson className="size-3" aria-hidden />
        {t('businessDomain.viewManifest')}
      </Button>
      {open && (
        <div className="mt-1.5 flex flex-col gap-1.5" data-testid="business-domain-manifest">
          {/* 免责声明是 spec 明文要求的一句：读者必须知道这段内容平台不解释。 */}
          <p className="text-meta text-muted-foreground">{t('businessDomain.manifestDisclaimer')}</p>
          <pre className="overflow-auto rounded-lg bg-muted/50 p-2.5 font-mono text-meta leading-relaxed">
            {JSON.stringify(declared, null, 2)}
          </pre>
        </div>
      )}
    </div>
  )
}

/** 单个域一行块：域名 + 状态徽章 + 计数 + 最近调用 + manifest 指纹与抽屉。 */
function DomainRow({ domain, manifest }: { domain: DomainStatus; manifest?: BusinessManifest }) {
  const { t } = useTranslation()
  const lastAt = domain.lastDispatchAt

  return (
    <li
      className={cn(
        'flex flex-col gap-1 px-3 py-2.5',
        // 降级域醒目标记（spec §4.2 的「降级标橙」）：左侧色条 + 淡红底，
        // 让「哪几个挂了」在长清单里一眼可见，不必逐行读徽章。
        !domain.available && 'border-l-2 border-status-danger bg-status-danger/[0.06]',
      )}
      data-testid={`business-domain-${domain.domain}`}
      data-available={domain.available ? 'true' : 'false'}
    >
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span className="font-mono text-xs font-medium text-foreground">{domain.domain}</span>
        {domain.available ? (
          <Badge variant="chip-ok">{t('businessDomain.available')}</Badge>
        ) : (
          <Badge variant="chip-bad">{t('businessDomain.degraded')}</Badge>
        )}
        {/* 计数行（spec §4.2 `调用 1523 次 / 失败 7 次`）：探针重启后计数归零，
            此时 0 次不代表「从没成功过」，故只在有调用时显示。 */}
        {domain.dispatchCount > 0 && (
          <span className="text-meta text-muted-foreground">
            {t('businessDomain.dispatchCount', {
              count: domain.dispatchCount,
              failures: domain.failureCount,
            })}
          </span>
        )}
        {/* 降级原因原样透传（PLUGIN_DISABLED / INIT_FAILED…），平台不改写不翻译。 */}
        {!domain.available && domain.degradedReason && (
          <span className="text-meta font-medium text-status-danger">
            {t('businessDomain.degradedReason', { reason: domain.degradedReason })}
          </span>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-muted-foreground">
        {lastAt > 0 ? (
          <span>{t('businessDomain.lastDispatch', { time: formatRelativeTime(lastAt * 1000) })}</span>
        ) : (
          <span>{t('businessDomain.neverDispatched')}</span>
        )}
        {domain.manifestDigest && (
          <span className="font-mono" title={domain.manifestDigest}>
            {t('businessDomain.manifestDigest', { digest: digestPreview(domain.manifestDigest) })}
          </span>
        )}
        <ManifestDrawer domain={domain} manifest={manifest} />
      </div>
    </li>
  )
}

export default function BusinessDomainStatus({
  status,
  manifest,
  loading = false,
  refreshing = false,
  error = false,
  onRefresh,
  className,
}: BusinessDomainStatusProps) {
  const { t } = useTranslation()
  const domains = status?.domains ?? []
  const degraded = domains.filter((d) => !d.available).length
  // 探针不可达且拿到的是最后一次已知状态（spec §5.1 / §6）：面板照常渲染，但必须显式告知。
  const showStale = !loading && !error && !!status?.stale

  return (
    <Panel
      className={className}
      title={
        domains.length > 0
          ? t('businessDomain.titleWithCount', { count: domains.length })
          : t('businessDomain.title')
      }
      icon={<Boxes className="size-3.5" />}
      // 有降级域时图标块转警示色：面板整体「有事」应能在页面上被扫到。
      tone={degraded > 0 ? 'warning' : 'primary'}
      actions={
        <RefreshButton
          variant="outline"
          iconSize="size-3.5"
          pending={refreshing}
          onClick={onRefresh}
          label={t('businessDomain.refresh')}
          className="h-7 rounded-full px-3 text-xs"
        />
      }
      bodyClassName="min-h-0 overflow-auto p-0"
    >
      {loading && (
        <div className="flex items-center gap-1.5 p-4 text-xs text-muted-foreground">
          <Loader2 className="size-3.5 animate-spin" aria-hidden />
          {t('common.loading')}
        </div>
      )}

      {!loading && error && (
        <div className="p-4 text-xs text-status-danger">{t('businessDomain.loadFailed')}</div>
      )}

      {/* 探针不可达：不隐藏面板，而是明确告知「这是最后一次已知状态」，
          否则运维会把过时数据当成实时结果。刷新按钮仍在，探针恢复后可直接取新值。 */}
      {showStale && (
        <div
          className="flex items-start gap-1.5 border-b bg-status-warning/10 px-3 py-2 text-meta text-status-warning"
          data-testid="business-domain-stale"
        >
          <AlertTriangle className="mt-px size-3.5 shrink-0" aria-hidden />
          <span>
            {t('businessDomain.staleNotice')}
            {status?.fetchedAt && (
              <span className="ml-1">
                {t('businessDomain.staleFetchedAt', { time: formatRelativeTime(status.fetchedAt) })}
              </span>
            )}
            {/* 后端在「查不到且无缓存」时会给出具体原因（如「节点未连接」），
                原样展示——它是排障的第一手线索，比通用提示有用得多。 */}
            {status?.error && (
              <span className="ml-1">{t('businessDomain.staleError', { error: status.error })}</span>
            )}
          </span>
        </div>
      )}

      {/* 空状态（spec §4.3）：明确「没注册任何业务域」并给出排查方向，
          而不是留白让运维以为面板坏了。 */}
      {!loading && !error && domains.length === 0 && (
        <EmptyState
          icon={<Boxes />}
          title={t('businessDomain.emptyTitle')}
          description={t('businessDomain.emptyDesc')}
        />
      )}

      {!loading && !error && domains.length > 0 && (
        <ul className="divide-y" data-testid="business-domain-list">
          {domains.map((d) => (
            <DomainRow key={d.domain} domain={d} manifest={manifest} />
          ))}
        </ul>
      )}
    </Panel>
  )
}
