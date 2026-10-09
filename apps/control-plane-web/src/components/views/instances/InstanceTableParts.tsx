import { useTranslation } from 'react-i18next'
import { ArrowUpDown, Wrench } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Checkbox } from '@jianmanager/ui/components/checkbox'
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
import { hasCapability, resolveCapabilities } from '@/lib/capabilities'
import type { InstanceInfo } from '@/lib/instance-types'

/**
 * 实例列表排序键（与 /instances/search 的 `sort` 参数对齐）。
 */
export type InstanceSortKey = 'name' | 'status' | 'createdAt' | 'nodeId'

/** 实例列表排序方向。 */
export type InstanceSortOrder = 'asc' | 'desc'

/** Radix Select 不接受空字符串 value：用哨兵值表示「全部 / 不过滤」。 */
const ALL = '__all__'
/** 角色统一语义色徽标（FR-136）：proxy 主色 / backend 次色 / beacon 提示色 / universal 中性。 */
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

/** 实例表头（平铺与分组视图复用）。含批量全选复选框（FR-058）与节点:端口列（FR-136）。 */
export function InstanceTableHeader({
  t,
  allSelected,
  onToggleAll,
  sortKey,
  sortOrder,
  onSort,
}: {
  t: (k: string, options?: Record<string, unknown>) => string
  allSelected: boolean
  onToggleAll: () => void
  sortKey: InstanceSortKey
  sortOrder: InstanceSortOrder
  onSort: (key: InstanceSortKey) => void
}) {
  return (
    <TableHeader className="bg-muted/50">
      <TableRow>
        <TableHead className="w-10">
          <Checkbox checked={allSelected} onCheckedChange={onToggleAll} aria-label={t('instanceBatch.selectAll')} />
        </TableHead>
        <SortableTableHead
          label={t('instances.name')}
          sortValue="name"
          currentSort={sortKey}
          currentOrder={sortOrder}
          onSort={onSort}
        />
        <TableHead>{t('instances.type')}</TableHead>
        <SortableTableHead
          label={t('instances.nodePort')}
          sortValue="nodeId"
          currentSort={sortKey}
          currentOrder={sortOrder}
          onSort={onSort}
        />
        <TableHead>{t('instances.role')}</TableHead>
        <TableHead>{t('grouping.tagsColumn')}</TableHead>
        <SortableTableHead
          label={t('instances.status')}
          sortValue="status"
          currentSort={sortKey}
          currentOrder={sortOrder}
          onSort={onSort}
        />
        <TableHead>{t('instances.actions')}</TableHead>
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
}: {
  label: string
  sortValue: InstanceSortKey
  currentSort: InstanceSortKey
  currentOrder: InstanceSortOrder
  onSort: (key: InstanceSortKey) => void
}) {
  const { t } = useTranslation()
  const active = currentSort === sortValue
  const orderLabel = currentOrder === 'desc' ? t('grouping.orderDesc') : t('grouping.orderAsc')
  return (
    <TableHead aria-sort={active ? (currentOrder === 'desc' ? 'descending' : 'ascending') : 'none'}>
      <button
        type="button"
        onClick={() => onSort(sortValue)}
        aria-label={active ? t('grouping.sortHeaderActive', { label, order: orderLabel }) : t('grouping.sortHeader', { label })}
        className="inline-flex items-center gap-1 rounded px-1 py-0.5 text-left transition-colors hover:bg-accent/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        <span>{label}</span>
        <ArrowUpDown className={cn('size-3.5', active ? 'text-primary' : 'text-muted-foreground/60')} aria-hidden="true" />
      </button>
    </TableHead>
  )
}

interface FilterOption {
  value: string
  label: string
}

/** 单个筛选下拉：含「全部」哨兵项 + 给定选项；无选项时禁用。 */
export function FilterSelect({
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
 * 实例行的「⋯」次要操作菜单（FR-138）：标签 / 资源限额 / 代理后端 / 克隆 / 删除收入下拉，
 * 行内只保留启停/重启主操作。运行态下克隆改禁用 + tooltip（非消失）；删除标红且运行态
 * 仍可用（FR-310：后端编排先停止再删除），tooltip 提示该行为。
 *
 * 导出供按键/能力画像门控的针对性测试直接挂载（避免整页渲染）。
 */
export function InstanceRowMenu({
  inst,
  onTags,
  onEditConfig,
  onLimits,
  onProxy,
  onClone,
  onAdoptRuntime,
  onDelete,
}: {
  inst: InstanceInfo
  onTags: () => void
  onEditConfig: () => void
  onLimits: () => void
  onProxy: () => void
  onClone: () => void
  /** 接管运行态漂移（FR-471）：仅在实例存在漂移时由调用方传入。 */
  onAdoptRuntime?: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  // 克隆要求实例已停止（运行/过渡态禁用并提示原因，而非隐藏）；
  // 删除运行态可用（FR-310 先停再删），仅以 tooltip 提示行为。
  const stopped = inst.status === 'STOPPED' || inst.status === 'CRASHED'

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="xs" aria-label={t('instances.moreActions')} className="px-1.5">
          ⋯
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem onSelect={onEditConfig}>{t('instances.editConfig')}</DropdownMenuItem>
        <DropdownMenuItem onSelect={onTags}>{t('grouping.editTags')}</DropdownMenuItem>
        {inst.processType === 'docker' && (
          <DropdownMenuItem onSelect={onLimits}>{t('instances.resourceLimit')}</DropdownMenuItem>
        )}
        {resolveCapabilities(inst).capabilities.includes('bcTopology') && (
          <DropdownMenuItem onSelect={onProxy}>{t('proxy.manageBackends')}</DropdownMenuItem>
        )}
        {/* 接管运行态漂移（FR-471）：工作目录下有未纳管活进程时出现；点击只开确认框，
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
