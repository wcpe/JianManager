import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Coffee, Copy, FolderOpen, Hexagon, MoreHorizontal, Pencil, Boxes, Radar, Download, Trash2, FileText } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import { Popover, PopoverContent, PopoverTrigger } from '@jianmanager/ui/components/popover'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from '@jianmanager/ui/components/context-menu'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { cn } from '@jianmanager/ui'
import {
  RUNTIME_TYPE_LABEL,
  countByType,
  deleteBlockedText,
  describeRefs,
  filterRuntimeInventory,
  type RuntimeInventoryRow,
  type RuntimeSourceFilter,
  type RuntimeTypeFilter,
} from '@/lib/nodes/runtime-inventory'

/**
 * 运行时清单表（JDK + Node.js + 其它，一张表）。
 *
 * 【为什么是一张表】见 lib/nodes/runtime-inventory 的头注：原先 JDK 面板与运行时面板
 * 是同一批数据的两半，靠 `filter(type !== 'jdk')` 互斥。合并后「类型」只是一个列值。
 *
 * 【为什么引用列是这次改造的重点】改造前没有任何地方显示「谁在用这个 JDK」，
 * 于是删除只能「先点后报 409」——把后端守卫当成交互流程的一部分。
 * 现在引用列直接显示实例数与绑定方式，删除按钮在 `deleteBlocked` 时**禁用并说明原因**
 * （判定来自服务端 `?withRefs=1`，与 Delete 守卫同源，前端不重算）。
 *
 * 【受控视图】不取数、不发请求、不弹 toast；所有动作经回调上报。
 */

/** 运行时类型图标。 */
function TypeIcon({ type, className }: { type: string; className?: string }) {
  if (type === 'jdk') return <Coffee className={className} />
  if (type === 'nodejs') return <Hexagon className={className} />
  return <Boxes className={className} />
}

export interface RuntimeInventoryTableProps {
  rows: RuntimeInventoryRow[]
  isLoading?: boolean
  /** 打开「新增运行时」抽屉（下载 / 登记 / 扫描三段在其内）。 */
  onOpenInstall: () => void
  onOpenScan: () => void
  onEdit: (row: RuntimeInventoryRow) => void
  onDelete: (row: RuntimeInventoryRow) => void
  onCopyPath: (path: string) => void
}

export function RuntimeInventoryTable({
  rows,
  isLoading,
  onOpenInstall,
  onOpenScan,
  onEdit,
  onDelete,
  onCopyPath,
}: RuntimeInventoryTableProps) {
  const { t } = useTranslation()
  const [query, setQuery] = useState('')
  const [type, setType] = useState<RuntimeTypeFilter>('all')
  const [source, setSource] = useState<RuntimeSourceFilter>('all')

  // 类型 chips 的数字按全量算（不是按已筛选结果），否则选中某类型后其它类型永远是 0。
  const counts = countByType(rows)
  const shown = filterRuntimeInventory(rows, { type, source, query })

  // 只列出现过的类型：节点上没装 Python 时不必给一个恒为 0 的 chip。
  const typeChips: Array<{ key: RuntimeTypeFilter; label: string; count: number }> = [
    { key: 'all', label: t('grouping.all', '全部'), count: counts.all ?? 0 },
    ...Object.keys(counts)
      .filter((k) => k !== 'all' && (counts[k] ?? 0) > 0)
      .map((k) => ({ key: k as RuntimeTypeFilter, label: RUNTIME_TYPE_LABEL[k] ?? k, count: counts[k] ?? 0 })),
  ]

  return (
    <div className="space-y-2" data-testid="runtime-inventory">
      {/* 工具条：搜索 + 新增入口。类型/来源 chips 在下一行，因为这一行已经有两颗按钮。 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t('nodes.jdkSearchPlaceholder', '搜索厂商 / 版本 / 路径')}
          className="h-8 min-w-0 flex-1"
          aria-label={t('nodes.jdkSearchPlaceholder', '搜索厂商 / 版本 / 路径')}
        />
        <Button variant="outline" size="sm" onClick={onOpenScan} data-testid="runtime-open-scan">
          <Radar className="size-4" />
          {t('nodes.runtimeLib.scan', '扫描发现')}
        </Button>
        <Button size="sm" onClick={onOpenInstall} data-testid="runtime-open-install">
          <Download className="size-4" />
          {t('nodes.runtime.addRuntime', '新增运行时')}
        </Button>
      </div>

      {/* 筛选 chips：类型 + 来源两组，各自独立。 */}
      <div className="flex flex-wrap items-center gap-1.5">
        {typeChips.map((c) => (
          <Chip key={c.key} active={type === c.key} onClick={() => setType(c.key)} count={c.count}>
            {c.label}
          </Chip>
        ))}
        <span className="mx-1 h-4 w-px bg-border" aria-hidden />
        {(
          [
            ['all', t('grouping.all', '全部')],
            ['managed', t('nodes.jdkManaged', '托管')],
            ['external', t('nodes.jdkExternal', '外部')],
          ] as const
        ).map(([key, label]) => (
          <Chip key={key} active={source === key} onClick={() => setSource(key)}>
            {label}
          </Chip>
        ))}
      </div>

      {/* 表体 */}
      {isLoading ? (
        <div className="space-y-1.5">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : shown.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          {rows.length === 0
            ? t('nodes.runtimeLib.empty', '尚无运行时')
            : t('nodes.jdkEmpty', '没有匹配的运行时')}
        </p>
      ) : (
        /* 用设计系统的 Table 原语（仓库禁用原生 table 标签）；refined 外观是「精工卡片」，
           更贴合清单类数据，其余 35 处表格零回归。 */
        <Table
          appearance="refined"
          containerClassName="rounded-md border"
          className="min-w-[720px] text-sm"
          data-testid="runtime-inventory-table"
        >
          <TableHeader>
            <TableRow className="text-[11px]">
              <TableHead className="px-2.5 py-1.5 text-left font-medium">{t('nodes.runtime.col.type', '类型')}</TableHead>
              <TableHead className="px-2.5 py-1.5 text-left font-medium">{t('nodes.runtime.col.name', '名称')}</TableHead>
              <TableHead className="px-2.5 py-1.5 text-left font-medium">{t('nodes.runtime.col.version', '版本')}</TableHead>
              <TableHead className="px-2.5 py-1.5 text-left font-medium">{t('nodes.runtime.col.source', '来源')}</TableHead>
              <TableHead className="px-2.5 py-1.5 text-left font-medium">{t('nodes.runtime.col.refs', '引用实例')}</TableHead>
              <TableHead className="px-2.5 py-1.5 text-left font-medium">{t('nodes.runtime.col.path', '路径')}</TableHead>
              <TableHead className="w-8 px-1 py-1.5" />
            </TableRow>
          </TableHeader>
          <TableBody>
              {shown.map((row) => (
                /*
                  每一行都可右键操作（与「⋯」菜单同一套动作）。
                  为什么两套入口都留：清单表是高频扫读的地方，右键是「停在某行直接动手」，
                  而「⋯」是给不知道右键的人的可发现性兜底。两者动作必须完全同源，
                  否则就会出现「右键能删、⋯不能删」这类分裂——所以 RowContextMenu 直接复用
                  RowMenu 的那份 blocked 判定。
                */
                <RowContextMenu
                  key={row.key}
                  row={row}
                  onEdit={onEdit}
                  onDelete={onDelete}
                  onCopyPath={onCopyPath}
                >
                <TableRow className="hover:bg-muted/30" data-testid={`runtime-row-${row.key}`}>
                  <TableCell className="px-2.5 py-1.5">
                    <span className="inline-flex items-center gap-1.5">
                      <TypeIcon type={row.type} className="size-3.5 text-muted-foreground" />
                      <span className="text-xs">{RUNTIME_TYPE_LABEL[row.type] ?? row.type}</span>
                    </span>
                  </TableCell>
                  <TableCell className="px-2.5 py-1.5">
                    <span className="font-medium">{row.name}</span>
                    {row.type === 'jdk' && (
                      <Badge variant="secondary" className="ml-1.5 bg-accent px-1.5 py-0 text-[10px] font-medium text-primary">
                        Java {row.majorVersion}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="px-2.5 py-1.5 text-xs text-muted-foreground tabular-nums">
                    {[row.version, row.arch].filter(Boolean).join(' · ') || '--'}
                  </TableCell>
                  <TableCell className="px-2.5 py-1.5">
                    {/* 来源用图标+文字，颜色区分托管/外部（与旧 JDK 面板的来源徽章同语义）。 */}
                    <span
                      className={cn(
                        'inline-flex items-center gap-1 whitespace-nowrap text-xs',
                        row.managed ? 'text-primary' : 'text-muted-foreground',
                      )}
                    >
                      {row.managed ? <Coffee className="size-3" /> : <FolderOpen className="size-3" />}
                      {row.managed ? t('nodes.jdkManaged', '托管') : t('nodes.jdkExternal', '外部')}
                    </span>
                  </TableCell>
                  <TableCell className="px-2.5 py-1.5">
                    <RefsCell row={row} />
                  </TableCell>
                  <TableCell className="max-w-[220px] px-2.5 py-1.5">
                    <button
                      type="button"
                      className="flex w-full items-center gap-1 truncate font-mono text-[11px] text-muted-foreground hover:text-foreground"
                      title={row.path}
                      onClick={() => onCopyPath(row.path)}
                    >
                      <span className="truncate">{row.path}</span>
                      <Copy className="size-3 shrink-0" />
                    </button>
                  </TableCell>
                  <TableCell className="px-1 py-1.5">
                    <RowMenu row={row} onEdit={onEdit} onDelete={onDelete} />
                  </TableCell>
                </TableRow>
                </RowContextMenu>
              ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}

/** 筛选胶囊。 */
function Chip({
  active,
  onClick,
  count,
  children,
}: {
  active: boolean
  onClick: () => void
  count?: number
  children: React.ReactNode
}) {
  return (
    <Button
      type="button"
      variant="invisible"
      size="auto"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-[11px] font-medium transition-colors',
        active
          ? 'border-primary/50 bg-accent text-primary'
          : 'border-border bg-card text-muted-foreground hover:bg-accent/60 hover:text-foreground',
      )}
    >
      {children}
      {typeof count === 'number' && <span className="font-semibold tabular-nums">{count}</span>}
    </Button>
  )
}

/**
 * 引用实例单元格。
 *
 * 三态：
 *  - 非 JDK（Node 等）→ 「—」：它们当前没有实例绑定语义，显示 0 会误导成「没人用」；
 *  - 有引用 → 可点数字，展开看实例名 + 绑定方式（direct / major 的含义差别很大）；
 *  - 无引用 → 灰色 0。
 */
function RefsCell({ row }: { row: RuntimeInventoryRow }) {
  const { t } = useTranslation()
  if (row.type !== 'jdk') {
    return (
      <span className="text-xs text-muted-foreground/60" title={t('nodes.runtime.refsNotApplicable', '该运行时类型没有实例绑定')}>
        —
      </span>
    )
  }
  if (row.refCount === 0) {
    return <span className="text-xs text-muted-foreground tabular-nums">0</span>
  }
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="invisible"
          size="auto"
          className="inline-flex items-center gap-1 rounded bg-accent px-1.5 py-0 text-xs font-semibold text-primary tabular-nums"
          data-testid={`runtime-refs-${row.key}`}
          title={describeRefs(row.refs)}
        >
          {row.refCount}
          <span className="font-normal text-primary/70">{describeRefs(row.refs)}</span>
        </Button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64 p-0">
        <div className="border-b px-2.5 py-1.5 text-[11px] font-medium text-muted-foreground">
          {t('nodes.runtime.refsTitle', { count: row.refCount, defaultValue: '{{count}} 个实例使用本 JDK' })}
        </div>
        <ul className="max-h-56 overflow-y-auto p-1">
          {row.refs.map((r) => (
            <li key={r.id} className="flex items-center gap-2 rounded px-1.5 py-1 text-xs hover:bg-muted/50">
              <span
                className={cn(
                  'shrink-0 rounded px-1 py-0 text-[10px] font-medium',
                  r.binding === 'direct' ? 'bg-primary/15 text-primary' : 'bg-muted text-muted-foreground',
                )}
                title={
                  r.binding === 'direct'
                    ? t('nodes.runtime.bindingDirect', '实例写死了这个 JDK，删除后它无法启动')
                    : t('nodes.runtime.bindingMajor', '实例按大版本解析，删除后可能解析到同版本其它 JDK')
                }
              >
                {r.binding === 'direct' ? t('nodes.runtime.binding.direct', '直接') : t('nodes.runtime.binding.major', '大版本')}
              </span>
              <span className="min-w-0 flex-1 truncate" title={r.name}>
                {r.name}
              </span>
              <span className="shrink-0 text-[10px] text-muted-foreground">{r.status}</span>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  )
}

/**
 * 行的右键菜单。
 *
 * 动作与「⋯」菜单完全同源：编辑登记信息 / 复制路径 / 打开文件位置 / 删除。
 * 删除项的禁用与原因说明复用 `deleteBlockedText`——**不重算**删除可行性，
 * 判定只来自服务端 `deleteBlocked`，避免两套入口给出不同答案。
 *
 * 快捷键提示（Enter / Delete）是装饰性的，不实际绑定键盘：
 * 表格行本身不是焦点单元，凭空给出可用的按键会误导。
 */
function RowContextMenu({
  row,
  onEdit,
  onDelete,
  onCopyPath,
  children,
}: {
  row: RuntimeInventoryRow
  onEdit: (row: RuntimeInventoryRow) => void
  onDelete: (row: RuntimeInventoryRow) => void
  onCopyPath: (path: string) => void
  children: React.ReactNode
}) {
  const { t } = useTranslation()
  const blockedText = deleteBlockedText(row)

  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>{children}</ContextMenuTrigger>
      <ContextMenuContent className="w-56">
        {/* 行标识：右键后先确认「我点的是哪一行」，尤其同厂商多版本时。 */}
        <div className="px-2 py-1.5 text-[11px] text-muted-foreground">
          {RUNTIME_TYPE_LABEL[row.type] ?? row.type} ·{' '}
          {row.type === 'jdk' ? `${row.name} ${row.majorVersion}` : row.name}
          {row.arch ? ` · ${row.arch}` : ''}
        </div>
        <ContextMenuSeparator />
        {row.type === 'jdk' && (
          <ContextMenuItem onSelect={() => onEdit(row)} data-testid={`runtime-ctx-edit-${row.key}`}>
            <Pencil className="mr-2 size-3.5" />
            {t('nodes.runtime.edit', '编辑登记信息')}
          </ContextMenuItem>
        )}
        <ContextMenuItem onSelect={() => onCopyPath(row.path)} data-testid={`runtime-ctx-copy-${row.key}`}>
          <Copy className="mr-2 size-3.5" />
          {t('nodes.runtime.copyPath', '复制路径')}
        </ContextMenuItem>
        {/*
          架构不是快捷键——之前把 row.arch 塞进 ContextMenuShortcut 是错的：
          那个位置按惯例渲染成右对齐的按键提示，用户会以为「amd64 是个快捷键」。
          架构信息改挂到行标识那行（它本来就该和版本一起说明「这是哪一份」）。
        */}
        <ContextMenuItem onSelect={() => onCopyPath(row.path)} data-testid={`runtime-ctx-locate-${row.key}`}>
          <FolderOpen className="mr-2 size-3.5" />
          {t('nodes.runtime.revealPath', '复制路径并定位')}
        </ContextMenuItem>
        <ContextMenuSeparator />
        {/* 删除：被引用时禁用并写明原因（与 ⋯ 菜单同一判定）。 */}
        <ContextMenuItem
          disabled={row.deleteBlocked}
          onSelect={() => onDelete(row)}
          className={cn(row.deleteBlocked ? 'text-muted-foreground' : 'text-status-danger')}
          data-testid={`runtime-ctx-delete-${row.key}`}
          title={blockedText ?? undefined}
        >
          <Trash2 className="mr-2 size-3.5" />
          {blockedText ?? t('common.delete', '删除')}
        </ContextMenuItem>
        {row.refCount > 0 && row.type === 'jdk' && (
          <ContextMenuItem disabled onSelect={() => {}}>
            <FileText className="mr-2 size-3.5" />
            {t('nodes.runtime.refsCount', { count: row.refCount, defaultValue: '{{count}} 个实例在用' })}
          </ContextMenuItem>
        )}
      </ContextMenuContent>
    </ContextMenu>
  )
}

/** 行操作菜单：编辑 / 复制路径 / 删除（删除被拦时禁用并说明原因）。 */
function RowMenu({
  row,
  onEdit,
  onDelete,
}: {
  row: RuntimeInventoryRow
  onEdit: (row: RuntimeInventoryRow) => void
  onDelete: (row: RuntimeInventoryRow) => void
}) {
  const { t } = useTranslation()
  const blockedText = deleteBlockedText(row)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-xs" aria-label={t('common.moreActions', '更多操作')} data-testid={`runtime-menu-${row.key}`}>
          <MoreHorizontal className="size-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-52">
        {row.type === 'jdk' && (
          <>
            <DropdownMenuItem onSelect={() => onEdit(row)} data-testid={`runtime-menu-edit-${row.key}`}>
              <Pencil className="mr-2 size-3.5" />
              {t('nodes.runtime.edit', '编辑登记信息')}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        )}
        {/* 删除前置：被引用时禁用并写明原因，而不是让用户「点了再吃 409」。 */}
        <DropdownMenuItem
          disabled={row.deleteBlocked}
          onSelect={() => onDelete(row)}
          className={cn(row.deleteBlocked ? 'text-muted-foreground' : 'text-status-danger')}
          data-testid={`runtime-menu-delete-${row.key}`}
          title={blockedText ?? undefined}
        >
          <Trash2 className="mr-2 size-3.5" />
          {blockedText ?? t('common.delete', '删除')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
