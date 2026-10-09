/**
 * @file LogsPageView：日志中心（多来源 / 联邦查询 / 强筛选 / 时间线）的受控视图，取数、联邦降级编排、
 *       导出下载、路由深链与在线/跟随轮询策略由应用容器负责。
 * @input lib/logs-filters（LOG_VIEWS / TIME_RANGE_PRESETS / logLevelStatus / computeVirtualWindow）、
 *        lib/console-log-types（LogEntry）、lib/logs-federation（类型投影 + 键常量）、
 *        Button/Input/Panel/StatusBadge/DropdownMenu/Select 原语、layout（PageShell/PageHeader/Segments）、
 *        views/instances/InstancePicker 插槽（由容器注入）、翻译上下文
 * @output LogsPageView、LogsPageViewProps、LogsFilterState、LogsFederationViewState、LogsInsights、
 *         LogsLevelFacet、LogsNodeOption、LogsInstancePickerArgs、LogsView
 * @sync apps/control-plane-web/src/pages/LogsPage.tsx、apps/control-plane-web/src/pages/LogsPage.dom.test.tsx、
 *        apps/control-plane-web/src/pages/LogsPage.federation.dom.test.tsx、
 *        apps/control-plane-web/src/pages/LogsPage.degradeReason.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-049 查看 / FR-050 检索 / FR-150 增强 + FR-482 覆盖/失败态接线）
 */
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, Download, Info, MoreHorizontal, Radio } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell, Segment, Segments } from '@jianmanager/ui/components/layout'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { cn } from '@jianmanager/ui'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import type { LogEntry } from '@/lib/console/console-log-types'
import { LOGS_FEDERATION_KEYS } from '@/lib/logs-federation/i18n'
import type { ClassifiedLogViewState, CoverageBannerProps, ExportDownloadAffordance } from '@/lib/logs-federation/types'
import { computeVirtualWindow, logLevelStatus, LOG_VIEWS, TIME_RANGE_PRESETS } from '@/lib/logs/logs-filters'
import type { LogExportScope, TimeRangePreset } from '@/lib/logs/logs-filters'

/** 日志中心主视图取值（与后端 `view` 参数同域；`legacy` 为只读存量入口）。 */
export type LogsView = (typeof LOG_VIEWS)[number]

/** Radix Select 不允许空字符串值，用哨兵代表「全部」。 */
const SENTINEL_ALL = '__all__'

/** 来源取值域（与 `logs.source_*` 文案键同域，故是闭集而非服务端枚举）。 */
const SOURCES = ['instance', 'control_plane', 'worker']

/** 级别快捷 pill 的取值域与顺序（全部 + 四级，顺序即展示顺序）。 */
const LEVELS = ['error', 'warn', 'info', 'debug']

/** 导出范围（顺序即下拉顺序）。 */
const EXPORT_SCOPES: LogExportScope[] = ['currentPage', 'allMatched', 'range']

// 虚拟滚动：固定行高 + 视口上下各预渲染 8 行。
const ROW_HEIGHT = 30
const OVERSCAN = 8
const VIEWPORT_HEIGHT = 460

/** 节点下拉候选（只声明用到的字段：容器直接传完整节点对象也结构兼容）。 */
export interface LogsNodeOption {
  id: number
  name: string
}

/** 级别维度 facet（洞察栏用；与后端 facets 的 values 元素同形）。 */
export interface LogsLevelFacet {
  value: string
  count: number
}

/**
 * 洞察栏数据（容器注入）。
 *
 * 只在**联邦活跃**时提供：`undefined` 表示当前列表不是联邦数据源，洞察栏仅保留占位高度。
 * 「是否有真实内容」由本视图按字段是否到达判定（原页判据），故容器不必另算一个布尔量——
 * 两处各判一次会在 stats/facets 先到其一时漂移。
 */
export interface LogsInsights {
  /** 当前视图事件总数（stats 各点 `agg.count` 之和）；`undefined` = stats 尚未到达。 */
  eventsCount?: number
  /** level 维度 facet（facets 尚未到达时为缺省空数组）。 */
  levelFacets?: LogsLevelFacet[]
}

/**
 * 联邦覆盖 / 失败态投影（容器从 `LogsFederationResult` 折算注入）。
 *
 * 只声明本视图渲染用到的字段，且全部复用包内 foundation 的类型：应用侧把整个
 * `LogsFederationResult` 直接传进来也结构兼容，无需把联邦 API 客户端类型迁进包。
 */
export interface LogsFederationViewState {
  /** 来源标记（`legacy` = 切换前存量数据）；null 表示无标记，标题区不渲染徽标。 */
  sourceTag: string | null
  /** 接口不可达 → 已降级经典表格（横幅按「不可达」文案，不误导为「未启用」）。 */
  degraded: boolean
  /** 联邦接口正常但引擎未启用（横幅按「可启用」文案给指引）。 */
  federatedNotReady: boolean
  /** 覆盖/失败态横幅（`classifyLogViewState` → `toCoverageBannerProps` 的产物）。 */
  banner: CoverageBannerProps
  /** 分类结果（空成功判定与游标结束提示的判据）。 */
  classified: ClassifiedLogViewState
  /** 导出下载门禁（阻断原因键只在此处解析成文案）。 */
  exportAffordance: ExportDownloadAffordance
}

/**
 * 实例筛选器插槽参数。
 *
 * 选择器本体在包内（`views/instances/InstancePicker`），但候选来自服务端搜索——
 * 「何时发请求、防抖多久、候选窗口多大」是外壳策略（千级实例不能一次拉全量），故由容器注入实现。
 */
export interface LogsInstancePickerArgs {
  /** 当前选中实例 id；null = 全部实例。 */
  value: number | null
  /** 选中变更上报（容器按「改筛选回第 1 页」处理）。 */
  onChange: (id: number | null) => void
  /** 平台视图下实例维度不适用（与节点下拉同步禁用）。 */
  disabled: boolean
}

/**
 * 筛选态（受控）。
 *
 * 全部维度都会进入查询键并触发重新取数，故 state 由容器持有、逐字段注入：
 * 视图不自行缓存筛选草稿（保持原页「输入即生效」的语义），只负责把改动经 `onChangeFilter` 上报。
 */
export interface LogsFilterState {
  /** 来源；空串 = 全部。仅 `all` 视图渲染该下拉。 */
  source: string
  /** 级别；空串 = 全部。 */
  level: string
  /** 节点 id；null = 全部。 */
  nodeId: number | null
  /** 实例 id；null = 全部（深链 `?instanceId=` 由容器折算进这里的初值）。 */
  instanceId: number | null
  /** 关键字（子串匹配 message）；空串 = 不过滤。 */
  keyword: string
  /** 时间范围预设。 */
  range: TimeRangePreset
}

/**
 * 受控边界（ADR-097 a 范式）：**不取数、不发请求、不弹 toast、不读路由**。
 * - 行数据、总数与三态（首屏加载 / 失败 / 就绪）经 props 注入：容器按 legacy / 联邦 / 经典三路择一注入 `items`；
 * - **归容器**的受控状态：筛选态 `filter` 与主视图 `view`（两者都是查询键）、页码 `page`、实时跟随 `follow`
 *   ——「改筛选/改视图/翻页/开关跟随都回第 1 页并清联邦游标」由容器在同一处落实（视图只上报意图）；
 *   `federation` 覆盖态、`insights`、节点候选、导出门禁与在途标志同为取数或副作用产物，一并注入；
 * - **留本组件**的纯 UI 状态：时间线的 `scrollTop` 与跟随态自动回顶——只影响本地渲染，不产生请求；
 * - 实例候选走 `renderInstancePicker` 插槽：选择器本体在包内，但候选来自服务端搜索，属外壳策略。
 */
export interface LogsPageViewProps {
  /** 已加载行（缺省空数组）；行序即数据源返回顺序，视图不再排序。 */
  items?: LogEntry[]
  /** 当前页条数口径的总数（legacy/经典 = 后端 total；联邦 = 当前页行数）。 */
  total?: number
  /** 取数中（含联邦覆盖探测）；决定时间线的空态判定——未就绪不得判成「暂无日志」。 */
  isLoading?: boolean
  /** 已拿到可展示的信封（首屏占位只在「取数中且无旧数据」时出现，避免改筛选时闪屏）。 */
  hasData?: boolean
  /** 取数失败；优先于列表渲染。 */
  isError?: boolean
  /** FR-482：false 时禁止把空列表呈成「暂无日志」成功空态（失败/partial 不得当空成功）。 */
  emptySuccessAllowed?: boolean
  /** 当前页（视图在跟随态下自行折为第 1 页）。 */
  page?: number
  /** 总页数（联邦按 nextCursor 推得，容器算好注入）。 */
  totalPages?: number
  /** 当前主视图（受控：它决定可见控件集合与查询键）。 */
  view: LogsView
  /** 当前筛选条件（受控：它是查询键）。 */
  filter: LogsFilterState
  /** 实时跟随开关状态（`aria-pressed` 与页脚实时指示同源）。 */
  follow?: boolean
  /** 节点下拉候选（容器取数注入）；缺省即只有「全部节点」一项。 */
  nodes?: LogsNodeOption[]
  /** 当前用户是否平台管理员：决定平台视图页签可见性与 Legacy 入口可用性。 */
  isPlatformAdmin?: boolean
  /** 联邦覆盖/失败态投影；`undefined` = 无联邦数据源（经典或 legacy 路径）。 */
  federation?: LogsFederationViewState
  /** 洞察栏数据；`undefined` = 非联邦数据源（仅保留占位高度）。 */
  insights?: LogsInsights
  /** 导出在途：按钮文案切「导出中…」并禁用。 */
  exporting?: boolean
  /** 导出门禁结果（容器判定：legacy / 空结果 / 覆盖校验未过）；视图只据此禁用按钮。 */
  exportDisabled?: boolean
  /** 导出被联邦覆盖门禁阻断（决定按钮 title 用阻断原因还是「下载前复核权限」）。 */
  exportBlocked?: boolean
  /** 筛选变更上报（只报被改动的字段）；「回第 1 页 + 清联邦游标」由容器落实。 */
  onChangeFilter: (patch: Partial<LogsFilterState>) => void
  /** 主视图切换（含平台/节点/全部/Legacy）。 */
  onChangeView: (view: LogsView) => void
  /** 实时跟随开关（轮询启停归容器）。 */
  onToggleFollow: () => void
  /** 上一页。 */
  onPrevPage: () => void
  /** 下一页（联邦游标推进归容器）。 */
  onNextPage: () => void
  /** 按范围导出（端点选择、下载与成败提示归容器）。 */
  onExport: (scope: LogExportScope) => void
  /** 覆盖态横幅的可操作引导（跳节点页 / 重开视图 / 仅查在线 / 联系管理员…）。 */
  onFederationAction: (key: string) => void
  /** 覆盖态横幅的重试（重取联邦与经典两侧，由容器决定）。 */
  onRetry: () => void
  /** 实例筛选器插槽（候选走服务端搜索，由外壳注入；必填）。 */
  renderInstancePicker: (args: LogsInstancePickerArgs) => ReactNode
}

/**
 * 日志中心（FR-049 查看 / FR-050 检索 / FR-150 增强 + FR-482 覆盖/失败态接线）。
 * 套「流水检索」范式：强筛选（级别 pill + 来源/节点/实例/时间范围 + 关键字）→ 时间线行（虚拟滚动）；
 * 「实时跟随」开关锁定首页并按间隔轮询（tail）；导出可选范围（当前页/全部匹配/时间段）。
 * 级别用 StatusBadge 着色，token 驱动，与告警页语义统一。
 *
 * FR-482 接线（最小改动）：
 * - 可选联邦 API（`/logs/federation`）由容器探测；404 → 降级 legacy 视图 + 横幅；
 * - 覆盖/失败态一律经 `classifyLogViewState` + `toCoverageBannerProps`，失败/partial 禁止空成功；
 * - `sourceTag=legacy` 打 Legacy 徽标；ErrFederatedNotReady 显示联邦未就绪说明；
 * - 导出在 `resolveExportDownloadAffordance` 阻断时禁用；404 降级保留经典 `/logs/export`。
 */
export function LogsPageView({
  items = [],
  total = 0,
  isLoading = false,
  hasData = false,
  isError = false,
  emptySuccessAllowed = true,
  page = 1,
  totalPages = 1,
  view,
  filter,
  follow = false,
  nodes,
  isPlatformAdmin = false,
  federation,
  insights,
  exporting = false,
  exportDisabled = false,
  exportBlocked = false,
  onChangeFilter,
  onChangeView,
  onToggleFollow,
  onPrevPage,
  onNextPage,
  onExport,
  onFederationAction,
  onRetry,
  renderInstancePicker,
}: LogsPageViewProps) {
  const { t } = useTranslation()

  const legacyView = view === 'legacy'
  const federationDegraded = federation?.degraded === true
  const federationNotReady = federation?.federatedNotReady === true
  // 洞察栏：事件数先解构出来（`insights` 未注入时两者自然为空），facet 缺省空数组。
  const eventsCount = insights?.eventsCount
  const levelFacets = insights?.levelFacets ?? []
  // 洞察栏是否有真实内容（决定是否挂 testid；占位容器本身始终渲染以稳定高度）。
  const insightsVisible = eventsCount !== undefined || levelFacets.length > 0

  return (
    // 阶段 6 页面迁移：外壳与页头改用布局层原语。
    // 保留 `h-full min-h-0 flex-col gap-4`——本页是视口自适应型（日志区内部滚动）。
    // 标题行原本带两个徽标/提示（来源标记、联邦降级），它们是标题的限定语，故一并进 title；
    // e2e 断言 heading「日志中心」是子串匹配，不受影响。
    <PageShell data-page="logs" className="h-full min-h-0 flex-col gap-4">
      <PageHeader
        title={
          <>
            {t('logs.title')}
            {/* 状态与提示固定在标题区：不占用列表空间，也不因状态变化推动下方布局。 */}
            {/* 徽标仅在确有来源标记时渲染：标题行是 flex，空占位只会让测试与读屏
                多拿到一个空节点，而它本身不承载高度，去掉不影响布局稳定。 */}
            {federation?.sourceTag && (
              <span data-testid="logs-source-tag" className="inline-flex shrink-0">
                <StatusBadge
                  level={federation.sourceTag === 'legacy' ? 'warning' : 'info'}
                  label={t(`logsFederation.sourceTag.${federation.sourceTag}`)}
                />
              </span>
            )}
            {(federationDegraded || federationNotReady) && (
              <span
                data-testid="logs-not-ready-hint"
                className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground"
                // 与横幅同源区分：只有引擎未启用才给「去启用」指引；接口不可达时
                // 让用户去开启是无效指令，只说可查范围与后续办法。
                title={
                  federationNotReady
                    ? t('logsFederation.degradedHint')
                    : t('logsFederation.degradedUnavailableHint')
                }
              >
                <Info className="size-3.5 shrink-0" />
                <span className="truncate">
                  {federationNotReady
                    ? t('logsFederation.degradedTitleShort')
                    : t('logsFederation.degradedUnavailableTitleShort')}
                </span>
              </span>
            )}
          </>
        }
        actions={
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                disabled={exportDisabled}
                data-testid="logs-export-trigger"
                title={
                  exportBlocked && federation?.exportAffordance.blockedKey
                    ? t(federation.exportAffordance.blockedKey)
                    : federation && !federation.degraded && federation.exportAffordance.showDownload
                      ? t(LOGS_FEDERATION_KEYS.exportPermissionRecheckHint)
                      : undefined
                }
              >
                <Download className="size-3.5" />
                {exporting ? t('logs.exporting') : t('logs.export')}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {EXPORT_SCOPES.map((scope) => (
                <DropdownMenuItem
                  key={scope}
                  disabled={scope === 'range' && filter.range === 'all'}
                  onSelect={() => onExport(scope)}
                >
                  {t(`logs.exportScope_${scope}`)}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        }
      />

      <div className="jm-toolbar-surface flex flex-wrap items-center gap-1 p-1">
        <Segments aria-label={t('logs.viewLabel')}>
          {LOG_VIEWS.filter(
            // legacy 是「切换前存量日志」的只读入口，不是平级主视图：收纳到右侧「更多」，
            // 避免与平台/节点视图并列造成误解，也减少工具栏元素数量。
            (candidate) =>
              candidate !== 'legacy' && (isPlatformAdmin || candidate === 'node_instance'),
          ).map((candidate) => (
            <Segment
              key={candidate}
              active={view === candidate}
              onClick={() => onChangeView(candidate)}
            >
              {t(`logs.view_${candidate}`)}
            </Segment>
          ))}
        </Segments>
        {/* 右侧收纳：非常用入口（Legacy 只读）进「更多」，保持主视图区稳定。 */}
        <div className="ml-auto flex items-center">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="sm"
                data-testid="logs-more-trigger"
                aria-label={t('logs.more')}
              >
                <MoreHorizontal className="size-4" />
                {t('logs.more')}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                data-testid="logs-view-legacy"
                disabled={!isPlatformAdmin}
                onSelect={() => onChangeView('legacy')}
              >
                {t('logs.view_legacy')}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {/* 强筛选工具栏 */}
      <div className="jm-toolbar-surface flex flex-wrap items-center gap-2 p-2">
        {/* 级别快速 pill（全部 + 四级） */}
        <div className="flex items-center gap-1">
          <LevelPill active={filter.level === ''} onClick={() => onChangeFilter({ level: '' })}>
            {t('logs.allLevels')}
          </LevelPill>
          {LEVELS.map((l) => (
            <LevelPill
              key={l}
              level={l}
              active={filter.level === l}
              onClick={() => onChangeFilter({ level: filter.level === l ? '' : l })}
            >
              {t(`logs.level_${l}`)}
            </LevelPill>
          ))}
        </div>

        <Input
          value={filter.keyword}
          onChange={(e) => onChangeFilter({ keyword: e.target.value })}
          placeholder={t('logs.searchPlaceholder')}
          className="h-9 w-52"
        />
        <Select
          value={filter.range}
          onValueChange={(v: string) => onChangeFilter({ range: v as TimeRangePreset })}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {TIME_RANGE_PRESETS.map((r) => (
              <SelectItem key={r} value={r}>
                {t(`logs.range_${r}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {view === 'all' && (
          <Select
            value={filter.source === '' ? SENTINEL_ALL : filter.source}
            onValueChange={(v: string) =>
              onChangeFilter({ source: v === SENTINEL_ALL ? '' : v })
            }
          >
            <SelectTrigger size="sm" className="w-32" data-testid="logs-source-select">
              <SelectValue placeholder={t('logs.allSources')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={SENTINEL_ALL}>{t('logs.allSources')}</SelectItem>
              {SOURCES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`logs.source_${s}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {/* 节点/实例选择器始终占位（platform 视图下禁用），避免切换视图时工具栏元素增减导致布局位移。 */}
        <Select
          disabled={view === 'platform'}
          value={filter.nodeId === null ? SENTINEL_ALL : String(filter.nodeId)}
          onValueChange={(v: string) =>
            onChangeFilter({ nodeId: v === SENTINEL_ALL ? null : Number(v) })
          }
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue placeholder={t('logs.allNodes')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('logs.allNodes')}</SelectItem>
            {nodes?.map((node) => (
              <SelectItem key={node.id} value={String(node.id)}>
                {node.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {/* 实例筛选：千级实例改走服务端搜索（InstancePicker），不再全量列举。
            Select 换掉的另一个原因：Radix Select 依赖全部 SelectItem mount 才能提供
            首字母跳转与方向键导航，因此无法只渲染前 N 项。 */}
        {renderInstancePicker({
          value: filter.instanceId,
          onChange: (id) => onChangeFilter({ instanceId: id }),
          disabled: view === 'platform',
        })}

        {/* 实时跟随开关 pill，靠右 */}
        <button
          type="button"
          aria-pressed={follow}
          onClick={onToggleFollow}
          disabled={legacyView}
          className={cn(
            'ml-auto inline-flex items-center gap-1.5 rounded-full px-3 py-1.5 text-xs font-medium transition-colors duration-200 ease-ios',
            follow
              ? 'bg-status-success/15 text-status-success'
              : 'bg-muted text-muted-foreground hover:bg-accent',
          )}
        >
          <Radio className={cn('size-3.5', follow && 'animate-pulse')} />
          {t('logs.follow')}
        </button>
      </div>

      {/* 洞察栏固定占位（高度恒为 min-h-8）：此前仅在联邦活跃且有数据时整条插入，
          进入页面后会「弹」出一条并把日志列表往下推。现改为始终渲染容器，内容为空时
          仅占位不显示任何文字，列表位置保持稳定。
          占位容器不挂 testid —— 否则「等洞察栏出现」会立刻命中空容器，读到的
          是尚未填充的空文本；挂 testid 的仍是有内容的那一份。 */}
      {!follow && (
        <div
          {...(insightsVisible ? { 'data-testid': 'logs-federation-insights' } : {})}
          className="flex min-h-8 flex-wrap items-center gap-2 border-y px-1 py-1 text-xs text-muted-foreground"
        >
          {eventsCount !== undefined && (
            <span>{t('logs.statsEvents', { count: eventsCount })}</span>
          )}
          {levelFacets.map((facet) => (
            <StatusBadge
              key={facet.value}
              level={logLevelStatus(facet.value.toLowerCase())}
              label={`${facet.value} ${facet.count}`}
            />
          ))}
        </div>
      )}

      {/* FR-482 覆盖/失败态横幅：partial/offline/failed 不得当空成功。 */}
      {federation && (
        <CoverageBanner federation={federation} onAction={onFederationAction} onRetry={onRetry} />
      )}

      {isLoading && !hasData ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-destructive">{t('logs.loadError')}</p>
      ) : (
        <Panel className="min-h-0 flex-1" bodyClassName="flex min-h-0 flex-col p-0">
          <LogTimeline
            items={items}
            total={total}
            follow={follow}
            loading={isLoading}
            emptySuccessAllowed={emptySuccessAllowed}
          />
          <LogFooter
            follow={follow}
            total={total}
            // 跟随态钉在第 1 页（最新），与容器的查询口径一致。
            page={follow ? 1 : page}
            totalPages={totalPages}
            onPrev={onPrevPage}
            onNext={onNextPage}
          />
        </Panel>
      )}
    </PageShell>
  )
}

/**
 * 覆盖/失败态横幅（FR-482）：消费 foundation 的 classify → toCoverageBannerProps。
 * visible=false 时不渲染；tone 驱动 token 配色；导出被阻断时展示原因。
 */
function CoverageBanner({
  federation,
  onRetry,
  onAction,
}: {
  federation: LogsFederationViewState
  onRetry: () => void
  onAction: (key: string) => void
}) {
  const { t } = useTranslation()
  const props: CoverageBannerProps = federation.banner
  const classified = federation.classified
  const sourceTag = federation.sourceTag
  const degraded = federation.degraded
  const federatedNotReady = federation.federatedNotReady
  // 404 降级始终展示横幅（即使 foundation 因 items 路径判定 visible=false）。
  const visible = props.visible || degraded || federatedNotReady
  if (!visible) return null

  // retry 有独立按钮与处理；其余可操作引导逐条落成按钮，避免计算了却不呈现。
  const guidanceActions = props.actionKeys.filter(
    (key) => key !== LOGS_FEDERATION_KEYS.actionRetry,
  )

  // 降级有两种来源，文案必须区分：
  // - federatedNotReady：联邦接口正常响应但引擎未启用 → 可给出启用指引；
  // - 纯 degraded：联邦接口本身不可达（404/未部署/连接失败）→ 不说「未启用」，
  //   否则会把「接口没部署」误导成「功能没开」，用户去开启也无效。
  const tone = degraded ? 'info' : federatedNotReady ? 'warning' : props.tone
  const title = degraded
    ? federatedNotReady
      ? t('logsFederation.federatedNotReady')
      : t('logsFederation.degradedUnavailable')
    : federatedNotReady
      ? t('logsFederation.federatedNotReady')
      : t(props.titleKey)

  return (
    <div
      data-testid="logs-coverage-banner"
      data-tone={tone}
      data-state={classified.state}
      data-degraded={degraded ? 'true' : 'false'}
      data-export-allowed={federation.exportAffordance.showDownload ? 'true' : 'false'}
      className={cn(
        'flex flex-col gap-1.5 rounded-md border px-3 py-2 text-sm',
        {
          'border-status-danger/40 bg-status-danger/10 text-status-danger': tone === 'danger',
          'border-status-warning/40 bg-status-warning/10 text-status-warning':
            tone === 'warning',
          'border-status-info/40 bg-status-info/10 text-status-info': tone === 'info',
          'border-status-success/40 bg-status-success/10 text-status-success':
            tone === 'success',
        },
      )}
      role="status"
    >
      <div className="flex flex-wrap items-center gap-2 font-medium">
        {tone === 'danger' || tone === 'warning' ? (
          <AlertTriangle className="size-4 shrink-0" />
        ) : (
          <Info className="size-4 shrink-0" />
        )}
        <span data-testid="logs-coverage-title">{title}</span>
        {sourceTag && (
          <span data-testid="logs-source-tag-banner" className="inline-flex">
            <StatusBadge
              level={sourceTag === 'legacy' ? 'warning' : 'info'}
              label={t(`logsFederation.sourceTag.${sourceTag}`)}
            />
          </span>
        )}
      </div>
      {(props.reasonKeys.length > 0 || classified.primaryReason) && (
        <ul className="flex flex-wrap gap-x-3 gap-y-0.5 text-xs opacity-90">
          {props.reasonKeys.map((key) => (
            <li key={key} data-testid="logs-coverage-reason">
              {t(key)}
            </li>
          ))}
        </ul>
      )}
      {!federation.exportAffordance.showDownload &&
        federation.exportAffordance.blockedKey && (
          <p data-testid="logs-export-blocked" className="text-xs">
            {t(federation.exportAffordance.blockedKey)}
          </p>
        )}
      {!classified.emptySuccessAllowed && (
        <p data-testid="logs-empty-not-success" className="text-xs opacity-80">
          {t('logsFederation.emptyNotSuccess')}
        </p>
      )}
      {classified.viewEnumerationEnded && !classified.historyFullyEnumerated && (
        <p data-testid="logs-next-cursor-hint" className="text-xs opacity-80">
          {t('logsFederation.export.nextCursorHint')}
        </p>
      )}
      {(props.showRetry || guidanceActions.length > 0) && !degraded && (
        <div className="flex flex-wrap gap-2">
          {props.showRetry && (
            <Button size="sm" variant="outline" onClick={onRetry} data-testid="logs-coverage-retry">
              {t('logsFederation.action.retry')}
            </Button>
          )}
          {guidanceActions.map((key) => (
            <Button
              key={key}
              size="sm"
              variant="outline"
              data-testid={`logs-coverage-action-${key}`}
              onClick={() => onAction(key)}
            >
              {t(key)}
            </Button>
          ))}
        </div>
      )}
    </div>
  )
}

/** 级别快速筛选 pill：选中态主色淡染，非选中态弱色；带级别时前导状态色点。 */
function LevelPill({
  level,
  active,
  onClick,
  children,
}: {
  level?: string
  active: boolean
  onClick: () => void
  children: ReactNode
}) {
  const status = level ? logLevelStatus(level) : null
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium transition-colors duration-200 ease-ios',
        active
          ? 'bg-primary/10 text-primary'
          : 'text-muted-foreground hover:bg-accent hover:text-foreground',
      )}
    >
      {status && (
        <span
          className={cn('size-1.5 rounded-full', {
            'bg-status-danger': status === 'danger',
            'bg-status-warning': status === 'warning',
            'bg-status-info': status === 'info',
            'bg-muted-foreground': status === 'neutral',
          })}
        />
      )}
      {children}
    </button>
  )
}

/**
 * 日志时间线（虚拟滚动）：固定行高，仅渲染视口附近的窗口，千行级日志不一次性入 DOM。
 * 跟随态下每次数据更新自动滚到顶部（最新在前）。
 */
function LogTimeline({
  items,
  total,
  follow,
  loading = false,
  emptySuccessAllowed = true,
}: {
  items: LogEntry[]
  total: number
  follow: boolean
  /** 查询尚未完成：此时不判空态，避免把「未就绪」误报为空结果。 */
  loading?: boolean
  /** FR-482：false 时禁止把空列表呈成「无日志」成功空态。 */
  emptySuccessAllowed?: boolean
}) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const [scrollTop, setScrollTop] = useState(0)

  // 跟随态：新数据到来时滚回顶部，保证最新行可见。
  useEffect(() => {
    if (follow && containerRef.current) {
      containerRef.current.scrollTop = 0
      setScrollTop(0)
    }
  }, [follow, items])

  const win = computeVirtualWindow({
    scrollTop,
    viewportHeight: VIEWPORT_HEIGHT,
    rowHeight: ROW_HEIGHT,
    total: items.length,
    overscan: OVERSCAN,
  })
  const visible = items.slice(win.startIndex, win.endIndex)

  // 查询尚未完成时不判空：交由上方 loading 分支呈现，避免把「未就绪」误报为空结果。
  if (items.length === 0 && !loading) {
    return (
      <div
        data-testid="logs-empty-state"
        data-empty-success={emptySuccessAllowed ? 'true' : 'false'}
        className="flex items-center justify-center text-sm text-muted-foreground"
        style={{ height: VIEWPORT_HEIGHT }}
      >
        {emptySuccessAllowed ? t('logs.empty') : t('logsFederation.emptyNotSuccess')}
      </div>
    )
  }

  return (
    <div
      ref={containerRef}
      onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
      data-testid="logs-virtual"
      data-total-count={total}
      className="min-h-0 flex-1 overflow-y-auto"
      style={{ maxHeight: VIEWPORT_HEIGHT }}
    >
      <div style={{ height: win.padTop }} />
      {visible.map((log) => (
        <LogRow key={log.id} log={log} />
      ))}
      <div style={{ height: win.padBottom }} />
    </div>
  )
}

/** 单条日志行（固定高度，等宽内容）：时间 + 级别徽标 + 来源 + 消息，hover 行高亮。 */
function LogRow({ log }: { log: LogEntry }) {
  const { t } = useTranslation()
  return (
    <div
      data-testid="log-row"
      className="flex items-center gap-3 border-b border-border/60 px-3 text-xs transition-colors hover:bg-accent/50"
      style={{ height: ROW_HEIGHT }}
    >
      <span className="w-40 shrink-0 font-mono text-[11px] text-muted-foreground">
        {new Date(log.time).toLocaleString()}
      </span>
      <StatusBadge
        level={logLevelStatus(log.level)}
        label={t(`logs.level_${log.level}`, log.level)}
        className="w-16 shrink-0 justify-center"
      />
      <span className="w-14 shrink-0 text-[11px] text-muted-foreground">
        {t(`logs.source_${log.source}`, log.source)}
      </span>
      <span className="min-w-0 flex-1 truncate font-mono text-[11px]" title={log.message}>
        {log.message}
      </span>
    </div>
  )
}

/** 时间线底部条：跟随态显实时指示；否则显分页。 */
function LogFooter({
  follow,
  total,
  page,
  totalPages,
  onPrev,
  onNext,
}: {
  follow: boolean
  total: number
  page: number
  totalPages: number
  onPrev: () => void
  onNext: () => void
}) {
  const { t } = useTranslation()
  if (follow) {
    return (
      <div className="flex shrink-0 items-center gap-2 border-t px-3 py-2 text-[11px] text-status-success">
        <span className="size-1.5 animate-pulse rounded-full bg-status-success" />
        {t('logs.followingHint')}
        <span className="ml-auto text-muted-foreground">{t('logs.totalCount', { count: total })}</span>
      </div>
    )
  }
  return (
    <div className="flex shrink-0 items-center justify-between border-t px-3 py-2 text-sm text-muted-foreground">
      <span>{t('logs.totalCount', { count: total })}</span>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={onPrev}>
          {t('logs.prevPage')}
        </Button>
        <span>{t('logs.pageInfo', { page, totalPages })}</span>
        <Button variant="outline" size="sm" disabled={page >= totalPages} onClick={onNext}>
          {t('logs.nextPage')}
        </Button>
      </div>
    </div>
  )
}

export default LogsPageView
