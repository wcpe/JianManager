/**
 * @file AuditPageView：审计日志检索页的受控视图，列表/总数/分页态、候选用户与导出触发由应用容器负责。
 * @input lib/audit-contracts（AuditLogInfo）、lib/audit-filters（AuditFilterState / formatAuditDetail）、
 *        lib/virtual-list（变高虚拟窗口）、Button/Input/Panel/Skeleton/Select 原语、
 *        layout（PageShell/PageHeader/ScopeBar/ListSkeleton）、翻译上下文
 * @output AuditPageView、AuditPageViewProps、AuditUserOption
 * @sync apps/control-plane-web/src/pages/AuditPage.tsx、apps/control-plane-web/src/pages/AuditPage.dom.test.tsx、
 *        apps/control-plane-web/src/pages/page-loading-skeleton.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-015 审计日志检索 + FR-158 行详情展开 + FR-172 分页信封 +
 *        FR-303 action 键翻译 + FR-321 失败留痕 + FR-496 阶段 3 作用域条 / 阶段 6 视口裁剪与同壳骨架）
 */
import { useLayoutEffect, useMemo, useRef, useState, type Ref } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { ChevronRight, Download } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { ListSkeleton, PageHeader, PageShell, ScopeBar } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import type { AuditLogInfo } from '@jianmanager/ui/lib/audit-contracts'
import { formatAuditDetail, type AuditFilterState } from '@jianmanager/ui/lib/audit-filters'
import { useVirtualRows } from '@jianmanager/ui/lib/virtual-list'
import { cn } from '@jianmanager/ui'

/**
 * Radix Select 不允许空字符串值，用哨兵代表「全部用户」。
 * 哨兵只在「控件值 ↔ 空串」之间做转换，故不会出现在上报给容器的筛选态里。
 */
const SENTINEL_ALL = '__all__'

/**
 * 行高估算（px，FR-496 阶段 6 视口裁剪用）。行高由行内单元格的行数唯一决定：
 * `px-3 py-2`(16) + 最高单元格 + `border-b`(1)，实测（Chromium 1440×900，root 16px）：
 * 单行 action 徽章 32.7 → 33；有翻译的 action 单元格叠两行（12px 标签 + 10px 原键角标）46.3 → 33+14。
 * 估算只用于「尚未量测到的行」撑滚动高度；两档行共用同一基准，故基准的绝对误差只会整体平移
 * 滚动区间，不会累积错位（错位只可能来自「两行/一行」判定错，故该判定与行渲染共用一处实现）。
 */
const ROW_BASE_PX = 33
const ROW_EXTRA_LINE_PX = 14
/** 展开行（详情正文长短不定）的估算高度：仅用于首帧与无布局环境，浏览器实测后以实测为准。 */
const ROW_EXPANDED_PX = 180
/** jsdom 无布局（clientHeight 恒 0）时的回退视口高，保证视口裁剪在测试里同样生效。 */
const FALLBACK_VIEWPORT_PX = 640

/**
 * FR-303：action 键 → 展示标签。有中文翻译时行内叠「翻译 + 原始 mono 角标」两行，
 * 无翻译时单行 `mono` 徽章。行渲染与行高估算共用此判定，避免两处判定漂移。
 */
function resolveActionLabel(t: TFunction, action: string): { label: string; showsRawKey: boolean } {
  const label = t(`audit.actions.${action}`, { defaultValue: action })
  return { label, showsRawKey: label !== action }
}

/** 候选用户（筛选下拉）：只声明用到的字段，容器直接传应用侧的用户列表也结构兼容。 */
export interface AuditUserOption {
  id: number
  username: string
}

/**
 * 受控边界（ADR-097 a 范式）：**不取数、不发请求、不弹 toast**。
 * - 已加载行、命中总数、列表三态与分页态经 props 注入（容器调 `useAuditLogs` 的无限分页）；
 * - **归容器**的受控状态：筛选五维 `filter`——它就是 `GET /audit` 的查询键，任一项变化都触发重新取数，
 *   且无限分页按 queryKey 天然回到第 1 页（「改筛选回第 1 页」由此保证，视图不参与分页游标）；
 * - 候选用户（`useUsers`）与导出（`exportAuditLogs` + Blob 下载）同为取数与副作用，归容器；
 *   「导出键」要携带同一份筛选但**不带分页参数**，只有持有 params 的容器算得出；
 * - **留本组件**的纯 UI 状态：展开行的目标 `expanded` 与其实测高度——展开只切换本地展示，不产生请求
 *   （详情来自行数据本身，无懒查），故不上提。
 */
export interface AuditPageViewProps {
  /**
   * 已加载的审计行（容器把分页结果摊平后注入）；缺省按空列表渲染。
   * 行序即后端返回顺序（created_at 倒序），视图不再排序。
   */
  logs?: AuditLogInfo[]
  /**
   * 命中总数（分页信封口径，含尚未加载的行）；`undefined` = 首屏信封尚未到达。
   * 仅用于页脚「已加载 X / 共 Y 条」与骨架分支判定，不参与分页游标。
   */
  total?: number
  /** 首屏取数中；仅当信封尚未到达时渲染同壳骨架（改筛选时保留旧行不闪屏）。 */
  isLoading?: boolean
  /** 列表取数失败；优先于列表渲染。 */
  isError?: boolean
  /** 候选用户（容器取数注入）；缺省即只有「全部用户」一项。 */
  users?: AuditUserOption[]
  /** 当前筛选条件（受控：它是查询键，改一项即触发重新取数并回到第 1 页）。 */
  filter: AuditFilterState
  /** 是否还有下一页（决定「加载更多」可用性）。 */
  hasNextPage?: boolean
  /** 下一页在途：禁用「加载更多」。 */
  isFetchingNextPage?: boolean
  /** 筛选维度变更：只上报被改动的字段，params 换算与重取由容器负责。 */
  onChangeFilter: (patch: Partial<AuditFilterState>) => void
  /** 清空全部筛选（恢复默认筛选态）。 */
  onResetFilter: () => void
  /** 追加下一页（分页窗口增长，已加载行累积保留）。 */
  onLoadMore: () => void
  /** 导出当前筛选命中的日志（NDJSON 下载由容器发起）；本组件只负责按钮禁用态。 */
  onExport: () => void
}

/**
 * 审计日志查询页（FR-015 + FR-158）。
 * 套「流水检索」范式：强筛选（用户/操作/目标类型/时间范围）→ 时间线行；行可展开看变更详情（detail）。
 * 「导出」走后端 NDJSON 白名单导出；「加载更多」逐页追加；「清空」恢复默认。
 * 后端分页 envelope 返回真实 total，页面展示已加载数与命中总数。
 */
export function AuditPageView({
  logs = [],
  total,
  isLoading = false,
  isError = false,
  users,
  filter,
  hasNextPage = false,
  isFetchingNextPage = false,
  onChangeFilter,
  onResetFilter,
  onLoadMore,
  onExport,
}: AuditPageViewProps) {
  const { t } = useTranslation()

  /** 展开的行 id；null 表示全部折叠（纯展示态，不影响取数）。 */
  const [expanded, setExpanded] = useState<number | null>(null)

  // 视口裁剪（FR-496 阶段 6）：数百行一次性进 DOM 是本页的主开销，只渲染滚动窗口内的行。
  // 展开行的详情正文长短不定（估算误差会让其后所有行偏移错位），故实测后回填。
  const expandedRef = useRef<HTMLDivElement>(null)
  const [expandedSize, setExpandedSize] = useState<{ id: number; px: number } | null>(null)
  const expandedPx = expandedSize && expandedSize.id === expanded ? expandedSize.px : ROW_EXPANDED_PX
  useLayoutEffect(() => {
    // jsdom 无布局：offsetHeight 恒 0 → 保留估算，测试里窗口完全确定。
    const px = expandedRef.current?.offsetHeight ?? 0
    if (expanded !== null && px > 0 && px !== expandedPx) setExpandedSize({ id: expanded, px })
  }, [expanded, expandedPx, logs])

  /** 逐行高度：折叠行按「一行/两行」估算，展开行用实测值 → 偏移前缀和贴近真实布局。 */
  const rowSizes = useMemo(
    () =>
      logs.map((log) =>
        log.id === expanded
          ? expandedPx
          : ROW_BASE_PX + (resolveActionLabel(t, log.action).showsRawKey ? ROW_EXTRA_LINE_PX : 0),
      ),
    [expanded, expandedPx, logs, t],
  )
  const { containerRef, onScroll, range } = useVirtualRows({
    total: logs.length,
    itemSize: ROW_BASE_PX,
    overscan: 6,
    fallbackViewportSize: FALLBACK_VIEWPORT_PX,
    sizes: rowSizes,
  })

  // 页脚计数以「已加载量」为真源，信封总数缺省（尚未到达）时退化成本地行数——与原页 `?? logs.length` 同口径。
  const totalCount = total ?? logs.length

  return (
    <PageShell>
      <PageHeader
        title={t('audit.title')}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={onExport} disabled={logs.length === 0}>
              <Download className="size-3.5" />
              {t('audit.export')}
            </Button>
            <Button variant="outline" size="sm" onClick={onResetFilter}>
              {t('audit.clear')}
            </Button>
          </>
        }
      />

      {/* 强筛选器：套作用域条（FR-496 阶段 3 布局规范） */}
      <ScopeBar>
        <Select
          value={filter.userId === '' ? SENTINEL_ALL : filter.userId}
          onValueChange={(v: string) => onChangeFilter({ userId: v === SENTINEL_ALL ? '' : v })}
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder={t('audit.allUsers')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('audit.allUsers')}</SelectItem>
            {users?.map((u) => (
              <SelectItem key={u.id} value={String(u.id)}>
                {u.username}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Input
          value={filter.action}
          onChange={(e) => onChangeFilter({ action: e.target.value })}
          placeholder={t('audit.actionPlaceholder')}
          className="h-9 w-44"
        />
        <Input
          value={filter.targetType}
          onChange={(e) => onChangeFilter({ targetType: e.target.value })}
          placeholder={t('audit.targetTypePlaceholder')}
          className="h-9 w-44"
        />
        <Input
          type="datetime-local"
          value={filter.from}
          onChange={(e) => onChangeFilter({ from: e.target.value })}
          aria-label={t('audit.from')}
          className="h-9 w-52"
        />
        <Input
          type="datetime-local"
          value={filter.to}
          onChange={(e) => onChangeFilter({ to: e.target.value })}
          aria-label={t('audit.to')}
          className="h-9 w-52"
        />
      </ScopeBar>

      {/* FR-496 阶段 6 补丁：数据未到时不再只显示一行「加载中」文字。页头与筛选条本来就先渲染，
          数据区这里用**同壳骨架**（同一张 Panel + 同一条吸附列头 + 行占位）顶上，
          数据到达后原地替换——期间没有整块内容突然出现，也没有列头/页脚的位移。 */}
      {isLoading && total === undefined ? (
        <Panel
          bodyClassName="p-0"
          footer={
            <>
              <Skeleton className="h-4 w-32" />
              <Skeleton className="h-8 w-20" />
            </>
          }
        >
          <div className="max-h-[calc(100vh-20rem)] overflow-auto">
            <AuditColumnHeader />
            <ListSkeleton rows={12} />
          </div>
        </Panel>
      ) : isError ? (
        <p className="text-destructive">{t('audit.loadError')}</p>
      ) : (
        <Panel
          bodyClassName="p-0"
          footer={
            <>
              <span>{t('audit.loadedCount', { loaded: logs.length, total: totalCount })}</span>
              <Button
                variant="outline"
                size="sm"
                disabled={!hasNextPage || isFetchingNextPage}
                onClick={() => onLoadMore()}
              >
                {t('audit.loadMore')}
              </Button>
            </>
          }
        >
          {/* 虚拟窗口的滚动容器（面板底部区留在容器外，始终可见） */}
          <div
            ref={containerRef}
            onScroll={onScroll}
            data-testid="audit-log-virtual"
            className="max-h-[calc(100vh-20rem)] overflow-auto"
          >
            {/* 列头：随窗口滚动吸附在顶部（否则滚下去就看不到列名） */}
            <AuditColumnHeader />
            {logs.length === 0 ? (
              <p className="px-3 py-10 text-center text-sm text-muted-foreground">{t('audit.empty')}</p>
            ) : (
              <>
                {/* 窗口上/下占位：窗口外的行用高度撑开，滚动条与整体高度保持真实 */}
                {range.before > 0 && <div aria-hidden="true" style={{ height: range.before }} />}
                {logs.slice(range.start, range.end).map((log, offset) => {
                  const index = range.start + offset
                  return (
                    <AuditRow
                      key={log.id}
                      log={log}
                      open={expanded === log.id}
                      isLast={index === logs.length - 1}
                      rowRef={expanded === log.id ? expandedRef : undefined}
                      onToggle={() => setExpanded((id) => (id === log.id ? null : log.id))}
                    />
                  )
                })}
                {range.after > 0 && <div aria-hidden="true" style={{ height: range.after }} />}
              </>
            )}
          </div>
        </Panel>
      )}
    </PageShell>
  )
}

/**
 * 列表列头（FR-496 阶段 6 补丁抽出）：内容是静态列名，不依赖数据，
 * 因此加载态与就绪态共用同一份——骨架期间列名/列宽就已就位，数据到达只是「行长出来」。
 */
function AuditColumnHeader() {
  const { t } = useTranslation()
  return (
    // 随窗口滚动吸附在顶部（否则滚下去就看不到列名）
    <div className="sticky top-0 z-10 flex items-center gap-3 border-b bg-muted/40 px-3 py-2 text-[11px] font-medium text-muted-foreground backdrop-blur-sm">
      <span className="w-4 shrink-0" />
      <span className="w-40 shrink-0">{t('audit.time')}</span>
      <span className="w-28 shrink-0">{t('audit.user')}</span>
      <span className="w-44 shrink-0">{t('audit.action')}</span>
      <span className="min-w-0 flex-1">{t('audit.target')}</span>
      <span className="w-28 shrink-0">{t('audit.ip')}</span>
    </div>
  )
}

/** 单条审计行：可点展开查看变更详情（detail）。 */
function AuditRow({
  log,
  open,
  isLast,
  rowRef,
  onToggle,
}: {
  log: AuditLogInfo
  open: boolean
  /** 末行不画分隔线：虚拟窗口下 `last:` 变体只看「已渲染的最后一个」，会误伤窗口末行。 */
  isLast: boolean
  /** 展开行挂给页面实测高度（仅展开时传入）。 */
  rowRef?: Ref<HTMLDivElement>
  onToggle: () => void
}) {
  const { t } = useTranslation()
  const detail = formatAuditDetail(log.detail)
  const hasDetail = detail !== '' || (log.failed && !!log.error)
  // FR-303：action 键翻译。已知键显翻译 + 原键角标，未知键回退原键（defaultValue），保证不崩。
  const { label: actionLabel, showsRawKey } = resolveActionLabel(t, log.action)
  return (
    <div ref={rowRef} className={cn('border-border/60', !isLast && 'border-b')}>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        disabled={!hasDetail}
        className={cn(
          'flex w-full items-center gap-3 px-3 py-2 text-left text-xs transition-colors',
          hasDetail ? 'hover:bg-accent/50' : 'cursor-default',
        )}
      >
        <ChevronRight
          className={cn(
            'size-4 shrink-0 text-muted-foreground transition-transform duration-200 ease-ios',
            open && 'rotate-90',
            !hasDetail && 'opacity-0',
          )}
        />
        <span className="w-40 shrink-0 font-mono text-[11px] text-muted-foreground">
          {new Date(log.createdAt).toLocaleString()}
        </span>
        <span className="w-28 shrink-0 truncate">{log.user?.username ?? `#${log.userId}`}</span>
        {/* FR-303：有翻译时显翻译 + 小号 mono 原键角标（筛选仍按原键，心智不断）；无翻译时保持原 mono 徽章。 */}
        <span className="w-44 min-w-0 shrink-0" title={log.action}>
          {showsRawKey ? (
            <span className="flex flex-col">
              <span className="truncate">{actionLabel}</span>
              <span className="truncate font-mono text-[10px] text-muted-foreground">{log.action}</span>
            </span>
          ) : (
            <span className="rounded bg-muted px-2 py-0.5 font-mono text-[11px]">{log.action}</span>
          )}
        </span>
        <span className="min-w-0 flex-1 truncate text-muted-foreground">
          {log.targetType && `${log.targetType}#${log.targetId}`}
        </span>
        {/* FR-321：失败操作红色徽章，一眼定位报错操作。 */}
        {log.failed && (
          <span className="shrink-0 rounded-full bg-destructive/10 px-2 py-0.5 text-[11px] font-medium text-destructive">
            {t('audit.failed')}
          </span>
        )}
        <span className="w-28 shrink-0 font-mono text-[11px] text-muted-foreground">{log.ip}</span>
      </button>
      {open && hasDetail && (
        <div className="bg-muted/30 px-3 pb-3 pl-10">
          {log.failed && log.error && (
            <>
              <p className="mb-1 text-[11px] font-medium text-destructive">{t('audit.errorContent')}</p>
              <pre className="mb-2 overflow-x-auto rounded-md border border-destructive/30 bg-destructive/5 p-2 font-mono text-[11px] whitespace-pre-wrap break-all text-destructive">
                {log.error}
              </pre>
            </>
          )}
          <p className="mb-1 text-[11px] font-medium text-muted-foreground">{t('audit.detail')}</p>
          <pre className="overflow-x-auto rounded-md border bg-card p-2 font-mono text-[11px] whitespace-pre-wrap break-all">
            {detail}
          </pre>
        </div>
      )}
    </div>
  )
}

export default AuditPageView
