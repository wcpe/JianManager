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
 * 工作台卡行高（px）：**声明值 = 真实行盒**，卡片行流与 `InstanceGroupManager` 右列表共用。
 *
 * 数值依据（Chromium 1440×900 实测卡片实高，逐段相加核对）：
 * 卡片实高 = 固定骨架 + 附加提示区高度 − 40（头部基础高，被提示区顶掉时不重复计）。
 * 固定骨架 = 32 内边距 + 2 边框 + 3×12 段间距 + 16 类型行 + 36 资源条 + 37.5 底部操作行
 *            + 26 大区·小区行（含 mt-2 与 text-micro 行盒）。
 *
 * 大区·小区由分组头改为卡面文字后，卡片多出约 26px；260 → 280 把它计入行盒：
 * 行盒 = 280 − 16 = 264 = 225.5 固定骨架 + 35 提示区上限 + 3.5 余量。
 * 提示区上限由卡片内的 `CARD_EXTRA_MAX_HEIGHT`（`max-height` 硬封）保证，
 * 故卡片实高由构造不超过行盒，不会与下一行重叠。
 *
 * 为什么不继续用 260：加上大区·小区行后 225.5 + 35 = 260.5 > 244（旧行盒），
 * 必然重叠；而「声明值 = 真实行盒」要求行盒是常量、不是各卡实高的最大值——
 * 卡片行流按本常量定位，行盒若随内容变长就会与下一行打架。
 */
/*
  【实测值，不是估算】卡片实高由内容决定，实测区间 169~229px（标签多/名称换行的更高）。
  这里取 272：卡片内部分区高度（含外边距）实测合计 233，加左右内边距 24 得 257，
  再加 CARD_ROW_GAP 8 = 265；取 272 留 7px 余量。
  **不要凭感觉调小这个值**：调小后卡片会被 overflow-hidden 裁掉内容（实测裁掉
  「节点:端口」那行的下半截），比重叠更难看。需要压缩卡片时，先减分区再改这里。

  【历史教训】此常量曾从 280 被调到 200（对应行盒 192），但卡片内容没跟着压缩，
  于是 229px 的卡片盖到下一行上，表现为「卡片重叠」。更糟的是上方注释仍写着 280/264，
  文档与代码已经脱节——所以这里改成**以实测值为基准**并写清来源。
  即便将来卡片变高，行盒内还有 overflow-hidden 兜底（见 renderCard 的包裹层），
  不会再次重叠；但那时应同步抬高本常量，而不是让内容被裁掉。
*/
export const CARD_ROW_HEIGHT = 272

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
 * 分组卡片视图（FR-136）：单个滚动容器 + 单个虚拟窗口承载「组头行 + 卡片行」的行流。
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
              // 行盒高度 = CARD_ROW_HEIGHT − CARD_ROW_GAP；`gap-4` 即 CARD_ROW_GAP（16），
              // 列间距与行间距同一口径。
              style={{ top, height: CARD_ROW_HEIGHT - CARD_ROW_GAP, gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))` }}
              className="absolute inset-x-0 grid gap-4"
            >
              {row.items.map((inst) => (
                /*
                  【卡片被钉在行盒高度内】虚拟化按 `CARD_ROW_HEIGHT - CARD_ROW_GAP` 这个常量
                  给每一行定位，而卡片内容是自适应的：实测卡片实高在 169~229px 之间浮动
                  （标签多的、名称换行的更高），而行盒只有 192px——超出的卡片直接盖住下一行，
                  表现为「卡片重叠」。这里给包裹层 `h-full overflow-hidden`：
                  卡实高不再决定行盒，行盒由常量决定，结构上不可能溢出。
                  卡片内部对超出内容做裁剪（见 InstanceWorktableCard 的 min-h-0）。
                */
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

/** proxy 行 inline 展开的已注册 backend 摘要（FR-136）。 */
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
