import { useEffect, useMemo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { instanceStatusLevel } from '@jianmanager/ui/lib/threshold'
import { useCardColumns } from '@/lib/hooks/use-card-columns'
import { useVirtualRows } from '@/lib/shared/virtual-list'
import {
  groupRowHeight,
  InstanceGroupHeaderContent,
  useStoredVirtualScroll,
  type InstanceTreeRow,
} from '@/components/views/instances/VirtualizedInstanceTables'
import { RoleBadge } from '@/components/views/instances/InstanceTableParts'
import type { InstanceInfo } from '@/lib/instances/instance-types'
import type { ProxyRegistration } from '@/lib/instances/proxy-registration'

/** 工作台卡渲染参数：由应用侧注入的卡片组件消费（该组件含启停等 mutation）。 */
export interface InstanceCardRenderArgs {
  inst: InstanceInfo
  nodeName: string
  roleBadge: ReactNode
  menu: ReactNode
  onOpen: () => void
}

/** proxy 行内联后端摘要的容器注入点。 */
export interface BackendsInlineProps {
  /** 已注册后端（容器经 useRegistrations 取数）。 */
  registrations?: ProxyRegistration[]
  /** 取数中。 */
  isLoading?: boolean
  /** 打开实例详情（容器注入路由跳转）。 */
  onOpenInstance: (id: number) => void
}

/**
 * 卡片行高与分组管理器共用。行盒 304px，容纳独立地址行、资源读数和有界异常提示；
 * 行距另留 8px。改变卡片内容高度时必须同步此值，不能靠裁掉底栏维持虚拟定位。
 */
export const CARD_ROW_HEIGHT = 312

/** 卡片行的行间与列间留白（px）：行盒 = CARD_ROW_HEIGHT − 本值，故行距与行盒不会各自漂移。 */
export const CARD_ROW_GAP = 8

/** 
 * 卡片行流里的一行：组头行独占一行，卡片行承载至多 `columns` 张卡。
 * 两者同属**一个**虚拟列表，故入 DOM 的卡数只与视口有关、与分组数无关。
 */
type CardFlowRow =
  | { kind: 'group'; key: string; group: Extract<InstanceTreeRow, { kind: 'group' }> }
  | { kind: 'cards'; key: string; items: InstanceInfo[] }

/** 
 * 分组卡片视图：单个滚动容器 + 单个虚拟窗口承载「组头行 + 卡片行」的行流。
 *
 * 【为什么合并成一个窗口】旧实现按分组维度分段，每组一段各自挂一个 `VirtualizedCardGrid`
 * （各带一个滚动容器与一个虚拟窗口），段数 = 顶层分组数，而每段都要各自铺满视口窗口——
 * 真机 Chromium（1440×900）实测 `?view=card&groupBy=region`（1200 实例 / 4 个大区）：
 * `instances-card-virtual` 容器 4 个、同时挂载 `instances-card-virtual-item` **79 张**
 * （21+21+21+16），是不分组表格视图（约 21 行）的 3.8 倍；每张卡自带 3 个 mutation hook
 * 与一个 10 秒轮询的指标查询，故挂载量 ≈ 常驻并发查询数。放大倍数正比于分组数：改前实测
 * node 维度（2 组）已是 2 容器 / 42 张，env/network/zone/type/role 等同理，组越多越糟。
 * 合并后各维度实测 16~21 张（同实测环境），与分组数无关。
 *
 * 行模型与表格视图**同源**：调用方传入 `buildInstanceTreeRows` 的行（组头 + 成员），
 * 折叠、大区/小区两级、多级分组树的行为因此与表格视图由构造保持一致（组件不另建一套）。
 * 逐行 `sizes` 与表格视图同理：组头 44 / 卡片行 CARD_ROW_HEIGHT，逐行高度直接写进行盒。
 */
export function VirtualizedGroupedCardView({
  rows,
  totalCount,
  loadedCount,
  onNeedMore,
  onToggleCollapse,
  scrollStorageKey,
  nodeName,
  buildMenu,
  onOpenInstance,
  renderCard,
  emptyLabel,
}: {
  /** 分组树行模型（与表格视图同源）：`buildInstanceTreeRows` 的输出。 */
  rows: InstanceTreeRow[]
  /** 服务端总数（写入 `data-total-count`，供虚拟化断言取数）。 */
  totalCount: number
  /** 当前已加载条数（与 `totalCount` 比较决定是否触发续加载）。 */
  loadedCount: number
  onNeedMore: () => void
  onToggleCollapse: (key: string) => void
  scrollStorageKey: string
  nodeName: (id: number) => string
  buildMenu: (inst: InstanceInfo) => ReactNode
  onOpenInstance: (id: number) => void
  /** 工作台卡渲染插槽：应用侧注入含 mutation 的卡片组件。 */
  renderCard: (args: InstanceCardRenderArgs) => ReactNode
  emptyLabel: string
}) {
  const columns = useCardColumns()

  /** 
   * 行模型 → 卡片行流：按 `columns` 把成员行打包成卡片行，组头行独立成行。
   * 遇组头必须先冲刷当前卡片行——组头总是独占一行（行序与表格视图一致），
   * 组头之后的成员从新的一行起排，不会与上一组的尾行拼在一起。
   */
  const flowRows = useMemo<CardFlowRow[]>(() => {
    const out: CardFlowRow[] = []
    let buffer: InstanceInfo[] = []
    const flush = () => {
      for (let i = 0; i < buffer.length; i += columns) {
        const items = buffer.slice(i, i + columns)
        const head = items[0]
        // i < buffer.length 保证分片非空，故首项恒存在；判空只为收窄类型（卡片行的 key 取首项 id）。
        if (head === undefined) continue
        out.push({ kind: 'cards', key: `cards:${head.id}`, items })
      }
      buffer = []
    }
    for (const row of rows) {
      if (row.kind === 'group') {
        flush()
        out.push({ kind: 'group', key: row.key, group: row })
      } else {
        buffer.push(row.instance)
      }
    }
    flush()
    return out
  }, [rows, columns])

  // 逐行高度：组头行比卡片行矮，故传数组（与 VirtualizedGroupedInstanceTable 同一做法）。
  // 组头再按层级分两档（顶层横幅 / 子层标签行），与行盒行内 style 同源。
  const sizes = useMemo(
    () => flowRows.map((row) => (row.kind === 'group' ? groupRowHeight(row.group.depth) : CARD_ROW_HEIGHT)),
    [flowRows],
  )
  const {
    containerRef,
    onScroll,
    range,
    offsets,
    totalSize,
  } = useVirtualRows({
    total: flowRows.length,
    itemSize: CARD_ROW_HEIGHT,
    sizes,
    overscan: 4,
    fallbackViewportSize: 720,
  })
  const handleScroll = useStoredVirtualScroll(containerRef, onScroll, scrollStorageKey)

  useEffect(() => {
    // 窗口尾部接近行流末尾即预取下一页（与表格视图同判据，只是单位换成了「行」）。
    if (range.end + 4 >= flowRows.length && loadedCount < totalCount) onNeedMore()
  }, [flowRows.length, loadedCount, onNeedMore, range.end, totalCount])

  return (
    <div
      ref={containerRef}
      onScroll={handleScroll}
      data-testid="instances-card-virtual"
      data-total-count={totalCount}
      /* 
        【高度交给 flex 分配，不再猜一个 calc】原来是 `max-h-[calc(100vh-18rem)]`：
        那个 18rem 是对「工具栏 + 筛选条 + 页头」高度的硬编码估算，只要上下新增一条
        （本轮加了分页条）就会算错——表现为卡片在底部被裁掉、却又不出现滚动条。
        现在容器是父级 flex 列里的 `flex-1` 子项，高度由布局真实算出；
        `min-h-0` 是 flex 子项可收缩、内部滚动生效的前提（缺了它内容会把容器撑高）。
      */
      className="min-h-0 flex-1 overflow-auto pr-1"
    >
      {/* 外层撑起模型总高，各显示行按 `offsets`（逐行高度前缀和）绝对定位 */}
      <div className="relative" style={{ height: totalSize }}>
        {flowRows.slice(range.start, range.end).map((row, i) => {
          const index = range.start + i
          const top = offsets?.[index] ?? index * CARD_ROW_HEIGHT
          if (row.kind === 'group') {
            return (
              <div
                key={row.key}
                data-testid="instances-card-group-row"
                data-group-depth={row.group.depth}
                data-collapsed={row.group.collapsed ? 'true' : 'false'}
                // 组头行高写进行盒：与逐行 sizes 用的是同一个常量，二者不会静默分叉。
                style={{ top, height: groupRowHeight(row.group.depth) }}
                // 底色由 `InstanceGroupHeaderContent` 自己按层级画（顶层横幅 / 子层无底色），
                // 外层这里不再铺底——否则子层会被套上一层灰块，又变回「三层等高」的样子。
                className="absolute inset-x-0 flex items-center px-3"
              >
                <InstanceGroupHeaderContent
                  label={row.group.label}
                  depth={row.group.depth}
                  count={row.group.count}
                  health={row.group.health}
                  collapsed={row.group.collapsed}
                  onToggle={() => onToggleCollapse(row.group.collapseKey)}
                />
              </div>
            )
          }
          return (
            <div
              key={row.key}
              // 行高与内外间隙共用常量，虚拟定位和真实网格不能各取一套值。
              style={{ top, height: CARD_ROW_HEIGHT - CARD_ROW_GAP, gap: CARD_ROW_GAP, gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}
              className="absolute inset-x-0 grid"
            >
              {row.items.map((inst) => (
                // 每张卡填满同一行盒；卡片内容变更时同步 CARD_ROW_HEIGHT，不能让内容盖到下一行。
                <div
                  key={inst.id}
                  data-testid="instances-card-virtual-item"
                  className="h-full min-h-0 overflow-hidden"
                >
                  {renderCard({
                    inst,
                    nodeName: nodeName(inst.nodeId),
                    roleBadge: <RoleBadge role={inst.role} compact />,
                    menu: buildMenu(inst),
                    onOpen: () => onOpenInstance(inst.id),
                  })}
                </div>
              ))}
            </div>
          )
        })}
        {/* 空态：无行可渲染时（无匹配 / 无实例）在原容器内给一行提示，保持外壳不塌。 */}
        {flowRows.length === 0 && (
          <p className="py-8 text-center text-muted-foreground">{emptyLabel}</p>
        )}
      </div>
    </div>
  )
}

/** proxy 行 inline 展开的已注册 backend 摘要。 */
export function BackendsInline({ registrations, isLoading = false, onOpenInstance }: BackendsInlineProps) {
  const { t } = useTranslation()

  if (isLoading) {
    return <p className="px-3 py-2 text-sm text-muted-foreground">{t('common.loading')}</p>
  }
  const regs = registrations ?? []
  if (regs.length === 0) {
    return <p className="px-3 py-2 text-sm text-muted-foreground">{t('proxy.noBackends')}</p>
  }
  return (
    <div className="px-3 py-2">
      <div className="mb-1.5 text-xs font-medium text-muted-foreground">
        {t('proxy.registeredBackends', { count: regs.length })}
      </div>
      <ul className="space-y-1">
        {regs.map((r) => {
          const b = r.backend
          return (
            <li key={r.id} className="flex items-center gap-3 text-sm">
              <span className="text-muted-foreground">{r.priority}</span>
              {b ? (
                // 行内跳转链接：与相邻纯文本同排，取 invisible + auto
                <Button
                  type="button"
                  variant="invisible"
                  size="auto"
                  className="font-medium text-primary hover:underline"
                  onClick={() => onOpenInstance(b.id)}
                >
                  {b.name}
                </Button>
              ) : (
                <span className="font-medium">{r.alias || `#${r.backendId}`}</span>
              )}
              {r.alias && <span className="text-xs text-muted-foreground">({r.alias})</span>}
              {b && (
                <StatusBadge
                  level={instanceStatusLevel(b.status)}
                  label={t(`instances.${b.status.toLowerCase()}`, b.status)}
                  className="ml-auto"
                />
              )}
              {b && b.serverPort > 0 && (
                <span className="text-xs tabular-nums text-muted-foreground">:{b.serverPort}</span>
              )}
              {!r.enabled && (
                <Badge variant="outline" className="text-muted-foreground">
                  {t('common.disabled')}
                </Badge>
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}
