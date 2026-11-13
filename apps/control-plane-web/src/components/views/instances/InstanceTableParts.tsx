import { cloneElement, memo, useRef, useState, type ReactElement } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowUpDown, Play, RotateCw, Square, Wrench } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
import { ColumnResizeHandle } from '@jianmanager/ui/components/column-resize-handle'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import {
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { hasCapability, resolveCapabilities } from '@/lib/instances/capabilities'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import {
  computeVisibleColumns,
  INSTANCE_OPTIONAL_COLUMNS,
  PINNED_COLUMNS,
  type InstanceColumnKey,
  type InstanceOptionalColumn,
} from '@/lib/instances/instance-view-prefs'

/** 表头拖拽用的 dataTransfer 类型（自定义 MIME：只接受本页发起的列拖拽，不误收外部拖入的文件/文本）。 */
const COLUMN_DRAG_MIME = 'application/x-jm-instance-column'

/** 列设置的缺省可见集（全开；缺省不改变既有表头结构）。 */
const ALL_OPTIONAL_COLUMNS: ReadonlySet<InstanceOptionalColumn> = new Set(INSTANCE_OPTIONAL_COLUMNS)

/** 
 * 实例列表排序键（与 /instances/search 的 `sort` 参数对齐）。
 */
export type InstanceSortKey = 'name' | 'status' | 'createdAt' | 'nodeId'

/** 实例列表排序方向。 */
export type InstanceSortOrder = 'asc' | 'desc'

/** Radix Select 不接受空字符串 value：用哨兵值表示「全部 / 不过滤」。 */
const ALL = '__all__'
/** 角色统一语义色徽标：proxy 主色 / backend 次色 / beacon 提示色 / universal 中性。 */
export function RoleBadge({ role, compact = false }: { role: string; compact?: boolean }) {
  const { t } = useTranslation()
  if (role === 'proxy') {
    return (
      <Badge variant="outline" className="border-primary/40 bg-accent text-primary">
        {t('networks.role_proxy')}
      </Badge>
    )
  }
  if (role === 'backend') {
    return (
      <Badge variant="outline" className="border-status-info/40 text-status-info">
        {t('networks.role_backend')}
      </Badge>
    )
  }
  if (role === 'beacon') {
    // 配套服务实例（非 MC 群组服角色）：用中性偏提示的样式与集群服角色区分开。
    return (
      <Badge variant="outline" className="border-status-warning/40 text-status-warning">
        {t('networks.role_beacon')}
      </Badge>
    )
  }
  if (compact) return null
  return <span className="text-muted-foreground text-xs">{t('networks.role_universal')}</span>
}

/** 
 * 可拖拽的表头单元格包装（用户提出「表头拖拽换位」）。
 *
 * 【为什么把拖拽能力放在这里而不是每列自己实现】8 个表头若各写一遍 draggable/onDragOver/
 * onDrop，样式与判据必然漂移。统一包一层后：钉住的列（首/尾）自动不可拖，
 * 其余列的视觉反馈（拖拽中半透明、目标列左侧高亮插入位）只有一份实现。
 *
 * 【为什么用原生 HTML5 DnD 而不是 dnd-kit 之类】需求只是「同层横排的几列换位置」，
 * 原生 API 的 dataTransfer 足够；引入 DnD 库会为这几个格子加约 30KB 依赖，
 * 且它的碰撞/排序模型在这里全是多余能力。
 */
function DraggableHead({
  columnKey,
  draggable,
  draggingKey,
  onDragStart,
  onDragEnd,
  onDragOverColumn,
  onDropColumn,
  resizable = false,
  resizeLabel,
  resizeTitle,
  onResize,
  onResizeEnd,
  onResetWidth,
  children,
}: {
  columnKey: InstanceColumnKey
  draggable: boolean
  draggingKey: InstanceColumnKey | null
  onDragStart: (key: InstanceColumnKey) => void
  onDragEnd: () => void
  onDragOverColumn: (key: InstanceColumnKey) => void
  onDropColumn: (key: InstanceColumnKey) => void
  /** 是否渲染右缘的列宽手柄；省略则不显示手柄。 */
  resizable?: boolean
  resizeLabel?: string
  resizeTitle?: string
  onResize?: (width: number) => void
  onResizeEnd?: () => void
  onResetWidth?: () => void
  /** 
   * **已经是 `<TableHead>` 的元素**（由 renderHead 产出）。
   * 本组件不自己再包一层 th：包了就会出现「一个列两个 th」，表头列数翻倍、
   * 与 tbody 的列数对不上（实测 14 个 th 对 8 列）。
   * 这里用 cloneElement 把拖拽属性合并进那唯一一个 th。
   */
  children: ReactElement
}) {
  const [over, setOver] = useState(false)
  const headProps = children.props as { className?: string; children?: React.ReactNode }
  const headChildren = resizable && onResize ? (
    <>
      {headProps.children}
      <ColumnResizeHandle
        testId={`instance-col-resize-${columnKey}`}
        label={resizeLabel ?? '调整列宽'}
        title={resizeTitle ?? '拖动调整列宽'}
        onResize={onResize}
        onResizeEnd={onResizeEnd}
        onReset={onResetWidth}
      />
    </>
  ) : (
    headProps.children
  )
  return cloneElement(children as ReactElement<Record<string, unknown>>, {
    'data-column-key': columnKey,
    'data-dragging': draggingKey === columnKey ? 'true' : undefined,
    'data-drag-over': over && draggingKey !== columnKey ? 'true' : undefined,
    children: headChildren,
    draggable: draggable || undefined,
    onDragStart: draggable
      ? (event: React.DragEvent) => {
          // 只带 key：列是可重排的语义单元，搬 DOM 片段没有意义。
          event.dataTransfer.setData(COLUMN_DRAG_MIME, columnKey)
          event.dataTransfer.effectAllowed = 'move'
          onDragStart(columnKey)
        }
      : undefined,
    onDragEnd: draggable ? onDragEnd : undefined,
    onDragOver: draggable
      ? (event: React.DragEvent) => {
          // 必须 preventDefault 才会触发 drop。
          event.preventDefault()
          event.dataTransfer.dropEffect = 'move'
          setOver(true)
          onDragOverColumn(columnKey)
        }
      : undefined,
    onDragLeave: draggable ? () => setOver(false) : undefined,
    onDrop: draggable
      ? (event: React.DragEvent) => {
          event.preventDefault()
          setOver(false)
          onDropColumn(columnKey)
        }
      : undefined,
    className: cn(
      headProps.className,
      'select-none',
      resizable && 'relative',
      draggable && 'cursor-grab active:cursor-grabbing',
      draggingKey === columnKey && 'opacity-40',
      // 插入位提示：左侧一条实色竖线（不用背景色，避免与表头 muted 底混淆）
      over && draggingKey !== columnKey && 'shadow-[inset_2px_0_0_0_var(--primary)]',
    ),
  })
}

/** 
 * 实例表头（平铺与分组视图复用）。含批量全选复选框与节点:端口列。
 *
 * 【列顺序由 `columnOrder` 驱动】表头此前是 8 个 `<th>` 硬编码顺序，用户无法调整。
 * 现在按 `columnOrder` 数组 map 渲染；行与 colgroup 用同一份顺序（见 InstanceRowView /
 * PaginatedInstanceTable），三处同源才不会错列。
 */
export function InstanceTableHeader({
  t,
  allSelected,
  onToggleAll,
  sortKey,
  sortOrder,
  onSort,
  selectAllLabel,
  visibleColumns = ALL_OPTIONAL_COLUMNS,
  columnOrder,
  onReorderColumn,
  onColumnResize,
  onColumnResizeEnd,
  onResetColumnWidth,
}: {
  t: (k: string, options?: Record<string, unknown>) => string
  allSelected: boolean
  onToggleAll: () => void
  sortKey: InstanceSortKey
  sortOrder: InstanceSortOrder
  onSort: (key: InstanceSortKey) => void
  /** 
   * 全选框的可访问名（含作用域说明）。列表是分页加载的，「全选」只覆盖**已加载页**，
   * 与页面上「已加载 N / 共 M」同源；由外壳按已加载/总数拼好传入，避免读屏用户
   * 把「全选」理解成「全库全选」而误用批量删除。缺省回落通用文案。
   */
  selectAllLabel?: string
  /** 可见的可选列（列设置）；缺省全开。固定列（勾选/名称/节点:端口/状态/操作）不受影响。 */
  visibleColumns?: ReadonlySet<InstanceOptionalColumn>
  /** 列顺序（拖拽换位结果）。缺省为默认顺序。 */
  columnOrder?: readonly InstanceColumnKey[]
  /** 列被拖动到目标列时的回调（省略则整表不可拖拽）。 */
  onReorderColumn?: (from: InstanceColumnKey, to: InstanceColumnKey) => void
  /** 拖动列宽时的实时回调；omit 则不渲染列宽手柄。 */
  onColumnResize?: (key: InstanceColumnKey, width: number) => void
  /** 列宽拖动结束（落 localStorage）。 */
  onColumnResizeEnd?: () => void
  /** 双击列宽手柄恢复该列默认宽度。 */
  onResetColumnWidth?: (key: InstanceColumnKey) => void
}) {
  const [dragging, setDragging] = useState<InstanceColumnKey | null>(null)
  /* 
    源列用 ref 兜底：state 更新是异步的，若用户在 dragStart 后极快地 drop（或测试里连续
    同步触发 dragStart→dragOver→drop），闭包里的 `dragging` 可能还是旧值（null）。
    ref 在 dragStart 里同步写入，drop 时必定读得到。
  */
  const dragSourceRef = useRef<InstanceColumnKey | null>(null)

  /* 
    列是否渲染 + 顺序：与行、colgroup 共用 computeVisibleColumns（三处同源，杜绝错列）。
  */
  const ordered = computeVisibleColumns(columnOrder, visibleColumns)
  const canDrag = Boolean(onReorderColumn)

  const renderHead = (key: InstanceColumnKey) => {
    switch (key) {
      case 'select':
        return (
          <TableHead className="w-10" key={key}>
            <Checkbox
              checked={allSelected}
              onCheckedChange={onToggleAll}
              aria-label={selectAllLabel ?? t('instanceBatch.selectAll')}
              title={selectAllLabel}
            />
          </TableHead>
        )
      case 'region':
        return <TableHead key={key}>{t('instances.regionZoneShort', { defaultValue: '区域' })}</TableHead>
      case 'name':
        return (
          <SortableTableHead
            key={key}
            label={t('instances.name')}
            sortValue="name"
            currentSort={sortKey}
            currentOrder={sortOrder}
            onSort={onSort}
          />
        )
      case 'identity':
        return <TableHead key={key}>{t('instances.identity', { defaultValue: '类型·标签' })}</TableHead>
      case 'nodePort':
        return (
          <SortableTableHead
            key={key}
            label={t('instances.nodePort')}
            sortValue="nodeId"
            currentSort={sortKey}
            currentOrder={sortOrder}
            onSort={onSort}
          />
        )
      case 'status':
        return (
          <SortableTableHead
            key={key}
            label={t('instances.status')}
            sortValue="status"
            currentSort={sortKey}
            currentOrder={sortOrder}
            onSort={onSort}
          />
        )
      case 'health':
        return (
          <TableHead key={key} align="right">
            {t('instances.health.column', { defaultValue: '健康度' })}
          </TableHead>
        )
      /* 
        业务插件列：可选列、**默认关**（见 instance-view-prefs 的说明），
        因为它会再占约 120px，默认全开会把「状态/操作」挤出视口。
        数据走批量端点（一次 ≤50 台），不逐行打探针。
      */
      case 'business':
        return <TableHead key={key}>{t('instances.business.column', { defaultValue: '业务插件' })}</TableHead>
      case 'actions':
        return <TableHead key={key}>{t('instances.actions')}</TableHead>
    }
  }

  // 表头是「列的定义」，应当比数据行更轻、比组头更淡：`bg-muted/50` 与组头的 `bg-muted/80`
  // 只差一档，滚动起来两者糊成一片。改为近乎底色 + 上缘一条实线，靠**线**而不是**块**分隔。
  return (
    <TableHeader className="border-t border-border/70 bg-muted/25">
      <TableRow>
        {ordered.map((key) => (
          <DraggableHead
            key={key}
            columnKey={key}
            draggable={canDrag && !PINNED_COLUMNS.includes(key)}
            draggingKey={dragging}
            resizable={Boolean(onColumnResize) && key !== 'select'}
            resizeLabel={t('instances.resizeColumnHint', { defaultValue: '拖动调整列宽' })}
            resizeTitle={t('instances.resizeColumnTitle', { defaultValue: '拖动调整列宽（双击恢复默认）' })}
            onResize={(width) => onColumnResize?.(key, width)}
            onResizeEnd={onColumnResizeEnd}
            onResetWidth={() => onResetColumnWidth?.(key)}
            onDragStart={(k) => {
              dragSourceRef.current = k
              setDragging(k)
            }}
            onDragEnd={() => {
              dragSourceRef.current = null
              setDragging(null)
            }}
            onDragOverColumn={() => {
              // 目标列由 drop 事件自己的 columnKey 提供，这里无需记录。
            }}
            onDropColumn={(target) => {
              // 源列优先取 ref（同步可靠），再退回 state（正常拖拽路径）。
              const from = dragSourceRef.current ?? dragging
              if (from && from !== target) onReorderColumn?.(from, target)
              dragSourceRef.current = null
              setDragging(null)
            }}
          >
            {renderHead(key)}
          </DraggableHead>
        ))}
      </TableRow>
    </TableHeader>
  )
}

export function SortableTableHead({
  label,
  sortValue,
  currentSort,
  currentOrder,
  onSort,
  children,
  ...rest
}: {
  label: string
  sortValue: InstanceSortKey
  currentSort: InstanceSortKey
  currentOrder: InstanceSortOrder
  onSort: (key: InstanceSortKey) => void
  /** 表头额外内容（列宽手柄由 DraggableHead 注入）。 */
  children?: React.ReactNode
} & Omit<React.ComponentProps<typeof TableHead>, 'children'>) {
  const { t } = useTranslation()
  const active = currentSort === sortValue
  const orderLabel = currentOrder === 'desc' ? t('grouping.orderDesc') : t('grouping.orderAsc')
  return (
    <TableHead aria-sort={active ? (currentOrder === 'desc' ? 'descending' : 'ascending') : 'none'} {...rest}>
      {/* 表头排序：外观是表头文字而非按钮，故 invisible + auto，
          手写的紧凑内边距与 hover 底色原样保留。
          显式写 text-[11px] font-semibold（= TableHead classic 档的继承值）：Button 基础类
          带 text-sm font-medium，不覆盖会把表头文字放大并减轻字重。 */}
      <Button
        type="button"
        variant="invisible"
        size="auto"
        onClick={() => onSort(sortValue)}
        aria-label={active ? t('grouping.sortHeaderActive', { label, order: orderLabel }) : t('grouping.sortHeader', { label })}
        className="inline-flex items-center gap-1 rounded px-1 py-0.5 text-left text-[11px] font-semibold transition-colors hover:bg-accent/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        <span>{label}</span>
        <ArrowUpDown className={cn('size-3.5', active ? 'text-primary' : 'text-muted-foreground/60')} aria-hidden="true" />
      </Button>
      {children}
    </TableHead>
  )
}

interface FilterOption {
  value: string
  label: string
}

/** 单个筛选下拉：含「全部」哨兵项 + 给定选项；无选项时禁用。 */
function FilterSelectImpl({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  options: FilterOption[]
}) {
  const { t } = useTranslation()
  return (
    <Select value={value} onValueChange={onChange} disabled={options.length === 0}>
      <SelectTrigger size="sm" className="w-40" aria-label={label}>
        <SelectValue placeholder={label} />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>
          {label}：{t('grouping.all')}
        </SelectItem>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** 
 * 筛选项记忆化。
 *
 * 【为什么】筛选条的 `options` 与 `onChange` 都是容器每次渲染现造的（`.map()` + 内联箭头），
 * 直接 `memo` 必然失效。这里按**内容**比较 `options`：只要候选集合没变，就不重建这个 Select。
 * 每个 Select 展开后是 60 个 `SelectItem` + Popper 上下文，重复重建代价很高——实测一次排序
 * 会因此产生 60 个 `SelectItem` 的重渲染。
 */
export const FilterSelect = memo(FilterSelectImpl, (a, b) => {
  if (a.label !== b.label) return false
  if (a.value !== b.value) return false
  const ao: FilterOption[] = a.options
  const bo: FilterOption[] = b.options
  if (ao.length !== bo.length) return false
  for (let i = 0; i < ao.length; i++) {
    const x = ao[i]
    const y = bo[i]
    if (!x || !y) return false
    if (x.value !== y.value || x.label !== y.label) return false
  }
  // onChange 是容器内联闭包，语义由 value 决定，不参与比较
  return true
})

/** 
 * 实例行的「⋯」次要操作菜单：标签 / 资源限额 / 代理后端 / 克隆 / 删除收入下拉，
 * 行内只保留启停/重启主操作。运行态下克隆改禁用 + tooltip（非消失）；删除标红且运行态
 * 仍可用（：后端编排先停止再删除），tooltip 提示该行为。
 *
 * 导出供按键/能力画像门控的针对性测试直接挂载（避免整页渲染）。
 */
export function InstanceRowMenu({
  inst,
  onStart,
  onStop,
  onRestart,
  onTags,
  onEditConfig,
  onLimits,
  onProxy,
  onClone,
  onAdoptRuntime,
  onDelete,
}: {
  inst: InstanceInfo
  /** 启动（非停止态不出现）。 */
  onStart?: () => void
  /** 停止（停止态不出现）。 */
  onStop?: () => void
  /** 重启（仅运行态出现）。 */
  onRestart?: () => void
  onTags: () => void
  onEditConfig: () => void
  onLimits: () => void
  onProxy: () => void
  onClone: () => void
  /** 接管运行态漂移：仅在实例存在漂移时由调用方传入。 */
  onAdoptRuntime?: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  // 克隆要求实例已停止（运行/过渡态禁用并提示原因，而非隐藏）；
  // 删除运行态可用（先停再删），仅以 tooltip 提示行为。
  const stopped = inst.status === 'STOPPED' || inst.status === 'CRASHED'

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="xs" aria-label={t('instances.moreActions')} className="px-1.5">
          ⋯
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {/*
          生命周期动作（启动 / 停止 / 重启）按状态出现在菜单顶部。
          列表行内只保留「当前状态最常用」的那一个按钮，其余落在这里——
          卡片视图没有行内按钮，三档全部从这里走，两个视图的可用动作因此一致。
        */}
        {stopped && onStart && (
          <DropdownMenuItem onSelect={onStart} data-testid="instance-menu-start">
            <Play className="size-3.5" /> {t('instances.start')}
          </DropdownMenuItem>
        )}
        {!stopped && onStop && (
          <DropdownMenuItem onSelect={onStop} data-testid="instance-menu-stop">
            <Square className="size-3.5" /> {t('instances.stop')}
          </DropdownMenuItem>
        )}
        {inst.status === 'RUNNING' && onRestart && (
          <DropdownMenuItem onSelect={onRestart} data-testid="instance-menu-restart">
            <RotateCw className="size-3.5" /> {t('instances.restart')}
          </DropdownMenuItem>
        )}
        {(onStart || onStop || onRestart) && <DropdownMenuSeparator />}
        <DropdownMenuItem onSelect={onEditConfig}>{t('instances.editConfig')}</DropdownMenuItem>
        <DropdownMenuItem onSelect={onTags}>{t('grouping.editTags')}</DropdownMenuItem>
        {inst.processType === 'docker' && (
          <DropdownMenuItem onSelect={onLimits}>{t('instances.resourceLimit')}</DropdownMenuItem>
        )}
        {resolveCapabilities(inst).capabilities.includes('bcTopology') && (
          <DropdownMenuItem onSelect={onProxy}>{t('proxy.manageBackends')}</DropdownMenuItem>
        )}
        {/* 接管运行态漂移：工作目录下有未纳管活进程时出现；点击只开确认框，
            真正下发由页面级 DangerConfirm 完成（与强杀同款：菜单不直接发写请求）。 */}
        {onAdoptRuntime && (
          <DropdownMenuItem onSelect={onAdoptRuntime} data-testid="instance-menu-adopt-runtime">
            <Wrench className="size-3.5" />
            {t('serverConsole.runtimeDriftAdopt')}
          </DropdownMenuItem>
        )}
        {/* 「可克隆」由能力画像 `clone` 承担（仅后端子服类实例声明），不复用 `role === 'backend'`
            硬编码，也不拿 `mcSemantics` 当门控（那是 MC 世界语义，proxy 无世界语义 ≠ 不可克隆）。 */}
        {hasCapability(resolveCapabilities(inst), 'clone') && (
          <DropdownMenuItem
            title={stopped ? undefined : t('instances.cloneRunningHint')}
            className={stopped ? undefined : 'opacity-50 cursor-not-allowed'}
            onSelect={(e) => {
              if (!stopped) {
                e.preventDefault()
                return
              }
              onClone()
            }}
          >
            {t('clone.action')}
          </DropdownMenuItem>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          variant="destructive"
          title={stopped ? undefined : t('instances.deleteRunningHint')}
          onSelect={onDelete}
        >
          {t('common.delete')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
