/**
 * @file AuditPageView：审计日志检索页的受控视图，列表/总数/分页态、候选用户与导出触发由应用容器负责。
 * @input lib/audit-contracts（AuditLogInfo）、lib/audit-filters（AuditFilterState / formatAuditDetail）、
 *        lib/virtual-list（变高虚拟窗口）、Button/Input/Panel/Skeleton/Select 原语、
 *        layout（PageShell/PageHeader/ScopeBar/ListSkeleton）、翻译上下文
 * @output AuditPageView、AuditPageViewProps、AuditUserOption
 * @sync apps/control-plane-web/src/pages/AuditPage.tsx、apps/control-plane-web/src/pages/AuditPage.dom.test.tsx、
 *        apps/control-plane-web/src/pages/page-loading-skeleton.dom.test.tsx
 * @since FR-508（组件受控化迁包；原页 FR-015 审计日志检索 + FR-158 行详情展开 + FR-172 分页信封 +
 *        FR-303 action 键翻译 + FR-321 失败留痕 + FR-502 阶段 3 作用域条 / 阶段 6 视口裁剪与同壳骨架）
 */
import { useLayoutEffect, useMemo, useRef, useState, type Ref } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { ChevronRight, Download } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { Input } from '@jianmanager/ui/components/input'
import { ListSkeleton, PageHeader, PageShell, ScopeBar } from '@jianmanager/ui/components/layout'
import { Panel } from '@jianmanager/ui/components/panel'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import type { AuditLogInfo } from '@/lib/audit/audit-contracts'
import { formatAuditDetail } from '@/lib/audit/audit-filters'
import type { AuditFilterState } from '@/lib/audit/audit-filters'
import { useVirtualRows } from '@/lib/shared/virtual-list'
import { mergeMeasuredRowHeights } from '@/lib/shared/row-heights'
import { cardSurface, cardSurfaceTone, cn } from '@jianmanager/ui'

/**
 * Combobox 用空串表示「未选」，无法承载一个可点选的「全部」项，故另设哨兵。
 * 哨兵只在「控件值 ↔ 空串」之间做转换，故不会出现在上报给容器的筛选态里。
 */
const SENTINEL_ALL = '__all__'

/**
 * 行高估算（px，FR-502 阶段 6 视口裁剪用）。行高由行内单元格的行数唯一决定：
 * `px-3 py-2`(16) + 最高单元格 + `border-b`(1)，实测（Chromium 1440×900，root 16px）：
 * 单行 action 徽章 32.7 → 33；有翻译的 action 单元格叠两行（12px 标签 + 10px 原键角标）46.3 → 33+14。
 *
 * 【只用于「尚未量测到的行」】估算值哪怕只差零点几像素也会**按行数线性累积**
 * （600 行 × 0.5px = 300px），滚动条位置会与真实内容错位。渲染过的行一律以 DOM 实测值回填
 * （见 `measuredRows`），估算只负责首帧与屏外行撑高度。
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
 * 受控边界（ADR-102 a 范式）：**不取数、不发请求、不弹 toast**。
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
  /** 候选用户（容器取数注入：服务端搜索的前 N 条）；缺省即只有「全部用户」一项。 */
  users?: AuditUserOption[]
  /** 服务端命中的用户总数：超过候选窗口时提示「已显示前 N 项」。 */
  usersTotal?: number
  /** 用户候选的键入查询上报（防抖与服务端请求由容器负责，与 InstancePicker 同款）。 */
  onUserQueryChange?: (keyword: string) => void
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
  usersTotal,
  onUserQueryChange,
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

  /**
   * 选中用户的展示名。
   *
   * 候选窗口是「前 N 条 + 键入服务端搜索」，选中的用户很可能不在当前窗口里；
   * 不记住名字，触发器就会退回显示裸的用户 id。纯展示状态，故留在视图。
   */
  const [pickedUser, setPickedUser] = useState<{ id: string; name: string } | null>(null)

  // 视口裁剪（FR-502 阶段 6）：数百行一次性进 DOM 是本页的主开销，只渲染滚动窗口内的行。
  // 展开行的详情正文长短不定（估算误差会让其后所有行偏移错位），故实测后回填。
  const expandedRef = useRef<HTMLDivElement>(null)
  const [expandedSize, setExpandedSize] = useState<{ id: number; px: number } | null>(null)
  const expandedPx = expandedSize && expandedSize.id === expanded ? expandedSize.px : ROW_EXPANDED_PX
  useLayoutEffect(() => {
    // jsdom 无布局：offsetHeight 恒 0 → 保留估算，测试里窗口完全确定。
    const px = expandedRef.current?.offsetHeight ?? 0
    if (expanded !== null && px > 0 && px !== expandedPx) setExpandedSize({ id: expanded, px })
  }, [expanded, expandedPx, logs])

  /**
   * 折叠行的实测高度（行 id → px）。
   *
   * 折叠行原先只有常量估算，而估算偏差**按行数累积**（600 行 × 0.5px = 300px），
   * 表现为滚动条位置与真实内容错位。这里把已渲染过的行实测回填，让前缀和随浏览收敛到真实布局
   * （与任务中心同一套机制，合并规则见 lib/shared/row-heights）。
   */
  const [measuredRows, setMeasuredRows] = useState<Map<string, number>>(() => new Map())
  /** 上次量测时的容器宽度：宽度一变（action 单元格折行数变），旧实测值整体失效。 */
  const measuredWidthRef = useRef<number>(-1)

  /** 逐行高度：展开行与已量测过的折叠行用实测值，其余用估算 → 偏移前缀和贴近真实布局。 */
  const rowSizes = useMemo(
    () =>
      logs.map((log) => {
        if (log.id === expanded) return expandedPx
        return (
          measuredRows.get(String(log.id)) ??
          ROW_BASE_PX + (resolveActionLabel(t, log.action).showsRawKey ? ROW_EXTRA_LINE_PX : 0)
        )
      }),
    [expanded, expandedPx, logs, measuredRows, t],
  )
  const { containerRef, onScroll, range, crossSize } = useVirtualRows({
    total: logs.length,
    itemSize: ROW_BASE_PX,
    overscan: 6,
    fallbackViewportSize: FALLBACK_VIEWPORT_PX,
    sizes: rowSizes,
  })

  useLayoutEffect(() => {
    const el = containerRef.current
    if (!el) return
    // 宽度变了就丢掉全部旧实测值重测：它们是按旧宽度量的，留着只会整体错位。
    // crossSize 自身经 400ms 防抖，拿到的是侧栏动画结束后的稳定宽度。
    const base = measuredWidthRef.current === crossSize ? measuredRows : new Map<string, number>()
    measuredWidthRef.current = crossSize
    const measured = [...el.querySelectorAll<HTMLElement>('[data-audit-row-id]')].map((node) => ({
      // jsdom 无布局：offsetHeight 恒 0，合并规则会自然忽略（测试里窗口完全确定）。
      id: node.dataset.auditRowId ?? '',
      px: node.offsetHeight,
    }))
    // 展开行自己有实测通道（expandedRef），故作为 skipId 传入。
    const { sizes, changed } = mergeMeasuredRowHeights(base, measured, expanded === null ? null : String(expanded))
    // 宽度变化时即使没有新实测值也要提交（把旧宽度下的实测值清掉）。
    if (changed || base !== measuredRows) setMeasuredRows(sizes)
  }, [containerRef, crossSize, expanded, measuredRows, range.end, range.start, logs])

  // 用户候选下拉：全部 + 服务端命中窗口 + 当前值回显兜底（选中项可能已被后续键入挤出窗口；
  // 仍在窗口内就不重复补，否则 Combobox 里会出现两个同值项）。
  const userOptions: ComboboxOption[] = [
    { value: SENTINEL_ALL, label: t('audit.allUsers') },
    ...(users ?? []).map((u) => ({ value: String(u.id), label: u.username })),
    ...(filter.userId !== '' &&
    pickedUser?.id === filter.userId &&
    !(users ?? []).some((u) => String(u.id) === filter.userId)
      ? [{ value: filter.userId, label: pickedUser.name }]
      : []),
  ]

  // 页脚计数以「已加载量」为真源，信封总数缺省（尚未到达）时退化成本地行数——与原页 `?? logs.length` 同口径。
  const totalCount = total ?? logs.length

  return (
    <PageShell>
      <PageHeader
        title={t('audit.title')}
        description={t('audit.subtitle')}
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

      {/* 强筛选器：套作用域条（FR-502 阶段 3 布局规范） */}
      <ScopeBar>
        {/*
          用户筛选用**服务端搜索**型选择器：候选只取前 N 条，键入经 `onUserQueryChange` 上报
          （容器 300ms 防抖后下发 `?q=`）。原先用 `useUsers()` 拉全量再逐个挂 SelectItem，
          正是规则点名的禁用形态：用户上百后每次 render 都要把整份选项列表求值一遍，
          且请求体随用户数线性增长。
          （Radix Select 依赖全部 SelectItem mount 才能提供方向键导航，故无法只渲染前 N 项；
          Combobox 基于 Popover——关闭即不挂 DOM，并有界渲染。）
        */}
        <div className="w-44">
          <Combobox
            ariaLabel={t('audit.allUsers')}
            options={userOptions}
            value={filter.userId === '' ? SENTINEL_ALL : filter.userId}
            onChange={(v) => {
              if (v === SENTINEL_ALL) {
                setPickedUser(null)
                onChangeFilter({ userId: '' })
                return
              }
              const picked = users?.find((u) => String(u.id) === v)
              setPickedUser({ id: v, name: picked?.username ?? v })
              onChangeFilter({ userId: v })
            }}
            placeholder={t('audit.allUsers')}
            className="w-full"
            allowCustom={false}
            onQueryChange={onUserQueryChange}
          />
          {/* 服务端截断时提示继续键入（与 InstancePicker / 群组成员候选一致）。 */}
          {usersTotal !== undefined && usersTotal > (users?.length ?? 0) && (
            <p className="mt-1 text-xs text-muted-foreground">
              {t('common.searchTruncated', { shown: users?.length ?? 0, total: usersTotal })}
            </p>
          )}
        </div>
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

      {/* FR-502 阶段 6 补丁：数据未到时不再只显示一行「加载中」文字。页头与筛选条本来就先渲染，
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
 * 列表列头（FR-502 阶段 6 补丁抽出）：内容是静态列名，不依赖数据，
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
    <div ref={rowRef} data-audit-row-id={log.id} className={cn('border-border/60', !isLast && 'border-b')}>
      {/*
        展开/收起整行：语义是一次「切换详情」的按钮动作，但视觉上是一行表格数据，
        因此走 invisible + auto（不自带任何外壳），样式全部由下方 className 决定。
        `justify-start` 去掉 Button 基础类的居中：这一行是左对齐的多列排布。
      */}
      <Button
        variant="invisible"
        size="auto"
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        disabled={!hasDetail}
        className={cn(
          'flex w-full items-center justify-start gap-3 px-3 py-2 text-left text-xs transition-colors',
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
      </Button>
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
          {/* 代码展示块保留 <pre>（承空白折叠与等宽语义），表面样式取 Card 同源常量：
              圆角/描边/底色/阴影由 packages/ui 一处决定（原 bg-card → solid 档）。 */}
          <pre
            className={cn(
              cardSurface,
              cardSurfaceTone.solid,
              'overflow-x-auto p-2 font-mono text-[11px] whitespace-pre-wrap break-all text-foreground',
            )}
          >
            {detail}
          </pre>
        </div>
      )}
    </div>
  )
}

export default AuditPageView
