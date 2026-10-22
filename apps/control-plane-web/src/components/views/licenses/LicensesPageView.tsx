/**
 * @file LicensesPageView：开源许可与依赖清单页的受控视图，清单取数与「返回」跳转由应用容器负责。
 * @input lib/licenses（依赖唯一键/包名过滤/分区计数）、布局层 PageShell/PageHeader、
 *         Panel/Input/Badge/Button/StatCard/表格原语、翻译上下文
 * @output LicensesPageView、LicensesPageViewProps、LicenseDepEntry
 * @sync apps/control-plane-web/src/pages/LicensesPage.tsx、apps/control-plane-web/src/pages/LicensesPage.dom.test.tsx
 * @since FR-508（组件受控化迁包；原页 FR-135 开源许可清单、FR-210 静态清单强断言）
 */
import { Fragment, useCallback, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, ChevronDown, ChevronRight, Package, ScrollText, Search, Wrench } from 'lucide-react'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { StatCard } from '@jianmanager/ui/components/stat-card'
import { Input } from '@jianmanager/ui/components/input'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { depKey, filterByName, partitionDeps } from '@/lib/licenses/licenses'
import { useVirtualRows } from '@/lib/shared/virtual-list'

/**
 * 依赖表主行的固定行高（px）。行盒由行内 style 钉死（「声明值即真实值」）：
 * 内容自然高 = 徽章/文本 20 + 单元格 `py-1.5`(12) + 行边框 1 ≈ 33 → 取 36 留余量。
 * 虚拟化按同一常量定位，故滚动映射与真实行盒严格一致、不随行数累积漂移。
 */
const LICENSE_ROW_PX = 36
/**
 * 展开行（许可证全文）的高度估算：仅用于首帧与无布局环境；浏览器实测后以实测值纠正
 * （同 `AuditPageView` 对不定高详情行的做法）。
 */
const LICENSE_TEXT_ROW_PX = 220
/** 展开行实测补偿：测量对象是单元格内的 div，行盒还要加单元格 `py-1.5`(12) 与行边框 1。 */
const LICENSE_TEXT_CELL_OVERHEAD_PX = 13
/** jsdom 无布局（clientHeight 恒 0）时的回退视口高，保证视口裁剪在测试里同样生效。 */
const FALLBACK_VIEWPORT_PX = 640

/**
 * 单条依赖的许可信息（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/licenses` 的 `LicenseDependency`：
 * 容器直接传静态清单解析出的完整对象也结构兼容（`scope` 在应用侧是字面量联合、此处放宽为
 * `string`），从而无需把该 API 类型迁进包（同 `ArtifactVersionsPageView` 的取舍）。
 */
export interface LicenseDepEntry {
  name: string
  version: string
  license: string
  author: string
  /** 依赖主页；仅 `https?://` 前缀渲染为可点链接。 */
  url: string
  /** 来源：web/bot-worker 为 npm，go 为 go.mod，client-updater 为 Gradle。 */
  scope: string
  /** 运行时依赖 vs 开发依赖（决定落在哪个分区表）。 */
  type: 'runtime' | 'dev'
  /** 许可证全文（可能为空，空则显示 `licenses.noLicenseText`）。 */
  licenseText: string
}

/**
 * 受控边界（ADR-102 b 范式）：**不取数、不碰路由**。
 * - 依赖清单与加载/错误态经 props 注入（容器调 `useLicenses()` 读静态资源 `/licenses.json`）；
 * - 页头「返回」以回调上报，由容器接 `navigate(-1)`；
 * - 包名搜索与行内展开是纯 UI 状态（不触发重新取数：过滤在本地对已取回的清单做），留在视图内。
 */
export interface LicensesPageViewProps {
  /** 依赖清单；缺省视作空清单（渲染空态面板）。 */
  dependencies?: LicenseDepEntry[]
  /** 清单生成时间（ISO 串）；用于页头副标题，缺省时不渲染副标题。 */
  generatedAt?: string
  /** 加载中：渲染 `common.loading` 文案。 */
  isLoading?: boolean
  /** 加载失败：与空清单同样渲染空态面板（与原页一致，不额外提示错误）。 */
  isError?: boolean
  /** 页头「返回」动作（应用侧接 `navigate(-1)`）。 */
  onBack: () => void
}

/** 开源许可与依赖清单页（FR-135）：搜索 + 运行时/开发分区计数（StatCard）+ 表格 + 行内展开许可证全文。 */
export function LicensesPageView({
  dependencies,
  generatedAt,
  isLoading = false,
  isError = false,
  onBack,
}: LicensesPageViewProps) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const deps = useMemo(() => dependencies ?? [], [dependencies])
  const filtered = useMemo(() => filterByName(deps, query), [deps, query])
  const part = useMemo(() => partitionDeps(filtered), [filtered])

  const toggle = (key: string) =>
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })

  return (
    // 全量对齐：外壳与页头改用布局层原语。原先无 data-page，迁移时补上。
    // 「返回」按钮从左侧移到 actions（页头右侧）——它与页名并列在左时，读屏顺序会先念
    // 按钮再念标题；放进操作区后标题始终是页头的第一项。
    // 迁包后按钮自身不改跳转语义：动作仍由容器注入的 onBack 决定（包内不依赖 react-router）。
    <PageShell data-page="licenses">
      <PageHeader
        title={t('licenses.title')}
        description={
          generatedAt
            ? t('licenses.generatedAt', { time: new Date(generatedAt).toLocaleString() })
            : undefined
        }
        actions={
          <Button variant="ghost" size="sm" onClick={onBack} className="gap-1.5">
            <ArrowLeft className="size-4" />
            {t('licenses.back')}
          </Button>
        }
      />

      <p className="text-sm text-muted-foreground">{t('licenses.subtitle')}</p>

      {!isLoading && !isError && deps.length > 0 && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <StatCard
            label={t('licenses.statTotal')}
            value={part.total}
            icon={<Package className="size-3.5" />}
          />
          <StatCard
            label={t('licenses.runtime')}
            value={part.runtimeCount}
            icon={<Package className="size-3.5" />}
            tone="success"
          />
          <StatCard
            label={t('licenses.dev')}
            value={part.devCount}
            icon={<Wrench className="size-3.5" />}
            tone="info"
          />
          <StatCard
            label={t('licenses.statLicenses')}
            value={part.licenseCount}
            icon={<ScrollText className="size-3.5" />}
          />
        </div>
      )}

      <div className="relative max-w-sm">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        {/* aria-label 落在可聚焦的 Input 自身（图标是装饰、无文本可作可达名）。 */}
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t('licenses.searchPlaceholder')}
          aria-label={t('licenses.searchPlaceholder')}
          className="pl-8"
        />
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        // 取数失败与空清单必须区分：报「暂无依赖数据」会让管理员以为构建期清单没生成。
        <Panel bodyClassName="py-10">
          <p className="text-center text-sm text-destructive" data-testid="licenses-error">
            {t('common.error')}
          </p>
        </Panel>
      ) : deps.length === 0 ? (
        <Panel bodyClassName="py-10">
          <p className="text-center text-sm text-muted-foreground">{t('licenses.empty')}</p>
        </Panel>
      ) : (
        <>
          <DependencyTable
            title={t('licenses.runtime')}
            icon={<Package className="size-3.5" />}
            tone="success"
            deps={part.runtime}
            expanded={expanded}
            onToggle={toggle}
          />
          <DependencyTable
            title={t('licenses.dev')}
            icon={<Wrench className="size-3.5" />}
            tone="info"
            deps={part.dev}
            expanded={expanded}
            onToggle={toggle}
          />
        </>
      )}
    </PageShell>
  )
}

/**
 * 展开的许可证全文行：内容长短不定，故渲染后把实测高度回填给虚拟模型
 * （jsdom 无布局时 offsetHeight 恒 0，保留估算，窗口在测试里完全确定）。
 */
function LicenseTextRow({
  id,
  text,
  onMeasure,
}: {
  /** 依赖唯一键（虚拟模型按它存实测高度）。 */
  id: string
  /** 许可证全文；空串渲染占位文案。 */
  text: string
  onMeasure: (id: string, px: number) => void
}) {
  const { t } = useTranslation()
  const ref = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const px = ref.current?.offsetHeight ?? 0
    if (px > 0) onMeasure(id, px + LICENSE_TEXT_CELL_OVERHEAD_PX)
  })

  return (
    <TableRow data-flat className="hover:bg-transparent" data-testid="license-text-row">
      <TableCell colSpan={5} className="bg-muted/30">
        <div ref={ref}>
          {text ? (
            <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-background p-3 text-xs leading-relaxed text-foreground/90">
              {text}
            </pre>
          ) : (
            <p className="text-xs text-muted-foreground">{t('licenses.noLicenseText')}</p>
          )}
        </div>
      </TableCell>
    </TableRow>
  )
}

/** 单分区依赖表（运行时 / 开发）：行内可展开查看许可证全文。 */
function DependencyTable({
  title,
  icon,
  tone,
  deps,
  expanded,
  onToggle,
}: {
  title: string
  icon: ReactNode
  tone: 'success' | 'info'
  deps: LicenseDepEntry[]
  expanded: Set<string>
  onToggle: (key: string) => void
}) {
  const { t } = useTranslation()

  /**
   * 展开行的实测高度（按依赖键存）。同一时刻可展开多行，故不是单个值。
   * 只有高度真的变了才写回，避免 useLayoutEffect 每次都触发一轮渲染。
   */
  const [textHeights, setTextHeights] = useState<Record<string, number>>({})
  const measureText = useCallback((id: string, px: number) => {
    setTextHeights((prev) => (prev[id] === px ? prev : { ...prev, [id]: px }))
  }, [])

  /**
   * 依赖表虚拟化：构建期清单实测 931 条（运行时 456 + 开发 475），此前两张表一次性进 DOM
   * （约 4650 个单元格）。行盒高度与逐行模型同源（见常量注释），窗口外的行用占位撑开。
   */
  const rowSizes = useMemo(
    () =>
      deps.map((d) => {
        const key = depKey(d)
        return LICENSE_ROW_PX + (expanded.has(key) ? textHeights[key] ?? LICENSE_TEXT_ROW_PX : 0)
      }),
    [deps, expanded, textHeights],
  )
  const { containerRef, onScroll, range } = useVirtualRows({
    total: deps.length,
    itemSize: LICENSE_ROW_PX,
    sizes: rowSizes,
    overscan: 8,
    fallbackViewportSize: FALLBACK_VIEWPORT_PX,
  })

  return (
    <Panel
      title={
        <span className="flex items-center gap-1.5">
          {title}
          <Badge variant="secondary" className="text-[10px] tabular-nums">
            {deps.length}
          </Badge>
        </span>
      }
      icon={icon}
      tone={tone}
      bodyClassName="p-0"
    >
      <div
        ref={containerRef}
        onScroll={onScroll}
        data-testid="licenses-table-virtual"
        className="max-h-[70vh] overflow-auto"
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8" />
              <TableHead>{t('licenses.colName')}</TableHead>
              <TableHead className="w-32">{t('licenses.colVersion')}</TableHead>
              <TableHead className="w-40">{t('licenses.colLicense')}</TableHead>
              <TableHead>{t('licenses.colAuthor')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {deps.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="py-6 text-center text-muted-foreground">
                  {t('licenses.empty')}
                </TableCell>
              </TableRow>
            ) : (
              <>
                {range.before > 0 && (
                  <TableRow aria-hidden="true" data-testid="licenses-virtual-spacer-before">
                    <TableCell colSpan={5} className="p-0" style={{ height: range.before }} />
                  </TableRow>
                )}
                {deps.slice(range.start, range.end).map((d) => {
                  const key = depKey(d)
                  const isOpen = expanded.has(key)
                  const isLink = /^https?:\/\//.test(d.url)
                  return (
                    <Fragment key={key}>
                      <TableRow
                        data-testid="license-row"
                        style={{ height: LICENSE_ROW_PX }}
                        className="cursor-pointer transition-colors duration-[var(--motion-duration-fast)] ease-ios hover:bg-accent/50"
                        onClick={() => onToggle(key)}
                      >
                        <TableCell className="text-muted-foreground">
                          {/* 展开开关是**真按钮**：此前整行靠 `<tr onClick>`，而 `<tr>` 不可聚焦、
                              也没有 role/键盘处理，键盘与读屏用户完全无法展开。
                              按钮带 aria-expanded 表达开合态；点击不再冒泡到行，避免双触发。 */}
                          {/* 纯图标开关 → ghost + icon-xs：悬停底色、焦点环与按压反馈由
                              Button 统一提供；尺寸取 size-5 与行高对齐（icon-xs 的 24px 会让 36px 行盒顶上多出空白）。 */}
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon-xs"
                            aria-expanded={isOpen}
                            aria-label={d.name}
                            data-testid="license-toggle"
                            onClick={(e) => {
                              e.stopPropagation()
                              onToggle(key)
                            }}
                            className="size-5 text-muted-foreground"
                          >
                            {isOpen ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
                          </Button>
                        </TableCell>
                        <TableCell className="font-medium">
                          <span className="flex items-center gap-2">
                            {isLink ? (
                              <a
                                href={d.url}
                                target="_blank"
                                rel="noreferrer"
                                onClick={(e) => e.stopPropagation()}
                                className="text-primary hover:underline"
                              >
                                {d.name}
                              </a>
                            ) : (
                              d.name
                            )}
                            <Badge variant="outline" className="shrink-0 text-[10px] font-normal">
                              {d.scope}
                            </Badge>
                          </span>
                        </TableCell>
                        <TableCell className="tabular-nums text-muted-foreground">{d.version || '—'}</TableCell>
                        <TableCell>
                          {d.license ? (
                            <Badge variant="secondary" className="font-normal">
                              {d.license}
                            </Badge>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </TableCell>
                        <TableCell className="max-w-64 truncate text-muted-foreground">{d.author || '—'}</TableCell>
                      </TableRow>
                      {isOpen && <LicenseTextRow id={key} text={d.licenseText} onMeasure={measureText} />}
                    </Fragment>
                  )
                })}
                {range.after > 0 && (
                  <TableRow aria-hidden="true" data-testid="licenses-virtual-spacer-after">
                    <TableCell colSpan={5} className="p-0" style={{ height: range.after }} />
                  </TableRow>
                )}
              </>
            )}
          </TableBody>
        </Table>
      </div>
    </Panel>
  )
}
