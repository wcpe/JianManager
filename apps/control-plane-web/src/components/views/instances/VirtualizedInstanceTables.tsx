/* eslint-disable react-refresh/only-export-components -- 视图与随其导出的纯逻辑/变体同文件（沿用原组织方式），非组件导出按仓库约定在此豁免 */
import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight } from 'lucide-react'
import {
  Table,
  TableBody,
  TableCell,
  TableRow,
} from '@jianmanager/ui/components/table'
import { Card } from '@jianmanager/ui/components/card'
import { Button } from '@jianmanager/ui/components/button'
import { useVirtualRows } from '@/lib/shared/virtual-list'
import { INSTANCE_EXPANDED_ROW_HEIGHT, INSTANCE_ROW_HEIGHT } from '@/components/views/instances/InstanceRowView'
import { memberHealth } from '@/lib/networks/topology'
import type { MemberHealth } from '@/lib/networks/topology'
import type { GroupDimension, InstanceGroup } from '@/lib/instances/instance-grouping'
import type { InstanceInfo } from '@/lib/instances/instance-types'
/** 虚拟滚动位置的 sessionStorage 键前缀（与 URL 组合成唯一键）。 */
export const SCROLL_KEY_PREFIX = 'jm.instances.scroll:'

/**
 * 分组头行高（px）：与实例行同样是**导出常量 = 行盒行内 style** 的「声明值即真实值」，
 * 供逐行 `sizes` 与分组头行共用。
 *
 * 【为什么分两档】顶层是横幅（有底色、上下留白），子层是轻量标签行（无底色、更紧凑）。
 * 两档高度差本身就是层级信号之一——若都用 44，层级只能靠颜色表达，弱到看不出区别。
 * 这也是虚拟化必须知道的事：`sizes` 逐行按 depth 取值，与行盒行内 style 严格同源，
 * 否则组头一高一矮会让滚动位置逐段漂移。
 */
export const INSTANCE_GROUP_ROW_HEIGHT = 48
/** 子层（小区 / 子组）组头行高：比顶层矮一档，视觉上明确「挂在上面那一条横幅下」。 */
export const INSTANCE_SUBGROUP_ROW_HEIGHT = 34

/** 按层级取组头行高（顶层横幅 / 子层标签行）。 */
export function groupRowHeight(depth: number): number {
  return depth === 0 ? INSTANCE_GROUP_ROW_HEIGHT : INSTANCE_SUBGROUP_ROW_HEIGHT
}

/**
 * 逐行高度模型（变高虚拟窗口）：与行盒的行内 style 同源——组头用组头常量、
 * 实例行用实例行常量、展开行按「主行 + 追加行」求和，三者都不从类名反推。
 *
 * 之所以必须逐行建模而不是单一 `itemSize`：树表里组头与实例行本来就不同高，
 * 且 proxy 展开会在同一「项」下**追加**一个 `<tr>`（INSTANCE_EXPANDED_ROW_HEIGHT）——
 * 用单一 itemSize 时这部分高度完全不计入占位，其后所有行的位置与滚动条长度都会漂移。
 */
function useInstanceRowsSizes(count: number, heightAt: (index: number) => number): readonly number[] {
  // `heightAt` 由调用方按「行模型 + 展开集合」记忆化，故本数组只在两者真的变化时重建。
  return useMemo(() => {
    const sizes = new Array<number>(count)
    for (let i = 0; i < count; i++) sizes[i] = heightAt(i)
    return sizes
  }, [count, heightAt])
}

/**
 * 实例「项」在虚拟模型里的高度（px）：主行恒在，展开时**追加**一个后端摘要行。
 * 与 `InstanceRowView` 里「展开时多渲染一个 `<tr>`、否则不渲染」同判据（isRowExpanded）。
 */
function instanceRowHeight(expanded: boolean): number {
  return INSTANCE_ROW_HEIGHT + (expanded ? INSTANCE_EXPANDED_ROW_HEIGHT : 0)
}

/**
 * 树表统一行模型（FR-452）：分组头行与成员实例行同属一个虚拟化列表，
 * 避免「每组一个虚拟表」的分片表头与滚动错位。
 * - group：分组头（可折叠），带成员计数与聚合健康色带；`depth` 区分 region 两级（0=大区 / 1=小区）。
 * - instance：成员实例行（沿用既有 `renderRow`）。
 */
export type InstanceTreeRow =
  | {
      kind: 'group'
      key: string
      /** 折叠态在 URL 中的稳定键（组头行自身用 key 渲染，与 collapseKey 解耦）。 */
      collapseKey: string
      label: string
      /** 层级深度（0 起）；region 两级为 0/1，groupTree 为分组树实际深度。 */
      depth: number
      count: number
      health: MemberHealth
      collapsed: boolean
    }
  | { kind: 'instance'; key: string; instance: InstanceInfo }

/**
 * 由分组结果 + 维度 + 折叠态构建树表行（FR-452）。
 * - region 维度：大区头（depth 0）→ 小区头（depth 1）→ 成员；折叠大区隐藏其全部小区。
 * - groupTree 维度：多级——组头（depth=树深度）→ 本组**直接**成员行 → 递归子组头 → 其成员；
 *   折叠某组隐藏其后代（子组头与全部成员行）。键为组 id，同名不同组各自成组。
 * - 其余维度：单级分组头 → 成员；`none` 维度直接平铺（无分组头）。
 * 未分组（空 key）由聚合函数保证排在末尾，此处不额外排序。
 */
export function buildInstanceTreeRows(
  groups: InstanceGroup[],
  dim: GroupDimension,
  labelOf: (key: string, level: number) => string,
  collapsed: Set<string>,
): InstanceTreeRow[] {
  if (dim === 'none') {
    return groups.flatMap((g) =>
      g.instances.map((instance) => ({ kind: 'instance' as const, key: `instance:${instance.id}`, instance })),
    )
  }
  const rows: InstanceTreeRow[] = []
  if (dim === 'region') {
    /*
      【大区/小区不再做层级】大区是虚拟分区（由 `region:` 标签推导），没有独立实体；
      做成「大区头 → 小区头 → 成员」两级嵌套后，真正的信息（哪台服）被两层组头压到屏幕下方，
      而两层组头的成员计数与健康色带又与卡片/行自身重复表达。

      现在 region 维度**不产出组头**：直接平铺成员行，大区·小区改由每张卡/每行的
      左上角文字承载（见 InstanceWorktableCard 与 InstanceRowView 的 region·zone 段）。
      行序仍按大区→小区排列（聚合函数已排序），只是不再画分隔行。
    */
    return groups.flatMap((region) =>
      (region.children ?? [{ key: '', instances: region.instances }]).flatMap((zone) =>
        zone.instances.map((instance) => ({
          kind: 'instance' as const,
          key: `instance:${instance.id}`,
          instance,
        })),
      ),
    )
  }
  if (dim === 'groupTree') {
    // 多级层级展开：组头 → 直接成员 → 子组（递归）；折叠即隐藏整棵子树。
    const walk = (list: InstanceGroup[], depth: number) => {
      for (const group of list) {
        const collapseKey = `${dim}:${group.key}`
        const groupCollapsed = collapsed.has(collapseKey)
        rows.push({
          kind: 'group',
          key: `group:${collapseKey}`,
          collapseKey,
          label: labelOf(group.key, depth),
          depth,
          count: group.instances.length,
          health: memberHealth(group.instances),
          collapsed: groupCollapsed,
        })
        if (groupCollapsed) continue
        for (const instance of group.direct ?? group.instances) {
          rows.push({ kind: 'instance', key: `instance:${instance.id}`, instance })
        }
        if (group.children && group.children.length > 0) walk(group.children, depth + 1)
      }
    }
    walk(groups, 0)
    return rows
  }
  for (const group of groups) {
    const collapseKey = `${dim}:${group.key}`
    const groupCollapsed = collapsed.has(collapseKey)
    rows.push({
      kind: 'group',
      key: `group:${collapseKey}`,
      collapseKey,
      label: labelOf(group.key, 0),
      depth: 0,
      count: group.instances.length,
      health: memberHealth(group.instances),
      collapsed: groupCollapsed,
    })
    if (groupCollapsed) continue
    for (const instance of group.instances) {
      rows.push({ kind: 'instance', key: `instance:${instance.id}`, instance })
    }
  }
  return rows
}

/** 分组头行的聚合健康色带（运行/过渡/崩溃/停止分段；FR-452 验收）。 */
function GroupHealthBand({ health }: { health: MemberHealth }) {
  const { t } = useTranslation()
  const segs: { value: number; className: string; label: string }[] = [
    { value: health.running, className: 'bg-status-success', label: t('networks.healthRunning') },
    { value: health.transitioning, className: 'bg-status-warning', label: t('networks.healthTransitioning') },
    { value: health.crashed, className: 'bg-status-danger', label: t('networks.healthCrashed') },
    { value: health.stopped, className: 'bg-muted-foreground/40', label: t('networks.healthStopped') },
  ]
  const summary = t('grouping.healthBand', {
    running: health.running,
    transitioning: health.transitioning,
    crashed: health.crashed,
    stopped: health.stopped,
  })
  return (
    <span
      className="flex h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-muted"
      role="img"
      aria-label={summary}
      title={summary}
      data-testid="instances-group-health"
    >
      {segs.map((s, i) =>
        s.value > 0 ? (
          <span
            key={i}
            className={s.className}
            style={{ width: `${(s.value / Math.max(health.total, 1)) * 100}%` }}
          />
        ) : null,
      )}
    </span>
  )
}

/**
 * 分组头内容（折叠按钮 + 组名 + 成员计数 + 聚合健康色带）：树表行与卡片行流**共用同一份**。
 *
 * 抽出来的理由：卡片视图此前是「每个分组一段、段头只有组名与计数、不可折叠」，与表格视图
 * 是两套实现——折叠、大区/小区两级（region）、多级分组树（groupTree）在这两种视图下会各自
 * 分叉。现在两处渲染同一份内容，行模型又同取 `buildInstanceTreeRows`，行为由构造保持一致。
 * 行盒高度仍由调用方写进行盒（表 44 / 卡片同样 44），本组件只负责行内布局。
 */
export function InstanceGroupHeaderContent({
  label,
  depth,
  count,
  health,
  collapsed,
  onToggle,
}: {
  label: string
  /** 层级深度（0 起）：region 两级为 0/1，groupTree 为分组树实际深度。 */
  depth: number
  count: number
  health: MemberHealth
  collapsed: boolean
  onToggle: () => void
}) {
  const { t } = useTranslation()
  const isTop = depth === 0
  /*
    【为什么重做第二遍】上一版给两层都加了层级徽标（「大区」「小区」）+ 竖向色条 + 计数，
    结果三层（r1 / z1 / z2）长得几乎一样，都是一行 44px 的等高块：
    用户读到的不是「大区里有小区」，而是「三个并列的路由条目」——反而更乱。

    根因是**三层同构**：层级靠装饰（颜色深浅、字号 1px）区分，而结构完全一样。
    这次改成**两种形态**，让层级由结构本身表达：

    - **顶层（大区 / 类型）= 横幅**：占满整行、有底色与上下留白，
      是这一段的「大标题」；它把下面的内容**包起来**，而不是与它们并列。
    - **子层（小区 / 服组）= 轻量标签行**：无底色、无边框，只有缩进 + 左侧细引导线 +
      小三角，读者一眼就知道「这是上面那条横幅的一部分」。

    这样即使不看文字，光凭「有底色 vs 无底色」就能分出层级。
  */
  return (
    <div
      className={
        isTop
          ? // 顶层：横向铺开，靠底色与字重立住「段标题」的身份。
            'flex min-w-0 items-center gap-2 rounded-md bg-muted/60 px-2.5 py-1.5 ring-1 ring-border/60 ring-inset'
          : // 子层：无底色，只有缩进与引导线，视觉上挂在顶层下面。
            'flex min-w-0 items-center gap-1.5 py-0.5'
      }
      style={isTop ? undefined : { paddingLeft: 10 + (depth - 1) * 18 }}
    >
      {/* 子层加一条竖向引导线，把「从属关系」画出来；顶层不需要。 */}
      {!isTop && <span aria-hidden="true" className="h-3 w-px shrink-0 bg-border" />}

      {/* 分组折叠箭头：纯图标按钮。两档尺寸（顶层 size-5 / 子层 size-4）必须由 className 保留——
          icon-xs 自带 size-6 会撑到 24px，把分组头行撑高、破坏虚拟滚动的定高模型，
          故取 invisible + auto 让 size-5/size-4 继续生效。 */}
      <Button
        type="button"
        variant="invisible"
        size="auto"
        onClick={onToggle}
        aria-expanded={!collapsed}
        aria-label={
          collapsed
            ? t('grouping.expandGroup', { name: label })
            : t('grouping.collapseGroup', { name: label })
        }
        data-testid="instances-group-toggle"
        className={
          isTop
            ? 'inline-flex size-5 shrink-0 items-center justify-center rounded text-foreground/70 transition-colors hover:bg-accent/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40'
            : 'inline-flex size-4 shrink-0 items-center justify-center rounded text-muted-foreground/70 transition-colors hover:bg-accent/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40'
        }
      >
        {collapsed ? (
          <ChevronRight className={isTop ? 'size-3.5' : 'size-3'} />
        ) : (
          <ChevronDown className={isTop ? 'size-3.5' : 'size-3'} />
        )}
      </Button>

      {/*
        名字与计数的排布也分两档：
        - 顶层：名字在前、计数紧随，字重 600 / 13px；
        - 子层：12px / 500，计数用更淡的等宽数字。
        刻意**不再放层级徽标**——层级已经由「有没有底色」表达了，
        再加一枚「大区/小区」标签属于同一信息说两遍（上一版正是栽在这里）。
      */}
      <span
        className={
          isTop
            ? 'truncate text-[13px] font-semibold tracking-tight text-foreground'
            : 'truncate text-[12px] font-medium text-foreground/75'
        }
      >
        {label}
      </span>

      <span
        className={
          isTop
            ? 'shrink-0 rounded bg-background px-1.5 font-mono text-[10.5px] tabular-nums text-muted-foreground'
            : 'shrink-0 font-mono text-[10.5px] tabular-nums text-muted-foreground/60'
        }
      >
        {count}
      </span>

      <GroupHealthBand health={health} />
    </div>
  )
}

/**
 * 实例分组树表（FR-452）：单个虚拟表承载「分组头行（可折叠）+ 成员实例行」。
 * 分组头行显示折叠箭头 + 分组名 + 成员计数 + 聚合健康色带；折叠态由父级写回 URL（`?collapsed=`）。
 *
 * 行高按逐行 `sizes` 建模（组头 44 / 实例行 36 / 展开时再追加一行 160），与行盒的行内 style
 * 同源，故滚动条长度与滚动位置映射严格贴合真实行盒。
 */
export function VirtualizedGroupedInstanceTable({
  rows,
  totalCount,
  loadedCount,
  onNeedMore,
  onToggleCollapse,
  scrollStorageKey,
  header,
  renderRow,
  emptyLabel,
  isRowExpanded,
  onVisibleRangeChange,
  columnCount = 11,
}: {
  rows: InstanceTreeRow[]
  totalCount: number
  loadedCount: number
  onNeedMore: () => void
  onToggleCollapse: (key: string) => void
  scrollStorageKey: string
  header: React.ReactNode
  renderRow: (inst: InstanceInfo) => React.ReactNode
  emptyLabel: string
  /** 展开行判据（与行渲染同源）：展开行多渲染一个 `<tr>`，须按展开行高计入模型。 */
  isRowExpanded?: (inst: InstanceInfo) => boolean
  /** 可见窗口上报（range 变化即通知）：容器据此切片实例列表批量取行内指标。 */
  onVisibleRangeChange?: (start: number, end: number) => void
  /** 当前表格总列数（列设置会改变它；占位/组头/空态的 colSpan 必须同步）。缺省 11。 */
  columnCount?: number
}) {
  const heightAt = useCallback(
    (index: number) => {
      const row = rows[index]
      // index 由虚拟窗口按 [0, rows.length) 给出，故 row 恒存在；判空只为收窄类型，
      // 兜底取实例行常量，与平铺表的「越界项按实例行占位」同判据。
      if (row === undefined) return INSTANCE_ROW_HEIGHT
      // 组头行高按层级取值（顶层横幅 / 子层标签行），与行盒行内 style 同源。
      if (row.kind === 'group') return groupRowHeight(row.depth)
      return instanceRowHeight(isRowExpanded?.(row.instance) === true)
    },
    [isRowExpanded, rows],
  )
  const sizes = useInstanceRowsSizes(rows.length, heightAt)
  const {
    containerRef,
    onScroll,
    range,
  } = useVirtualRows({
    total: rows.length,
    itemSize: INSTANCE_ROW_HEIGHT,
    sizes,
    overscan: 10,
    fallbackViewportSize: 520,
  })
  const handleScroll = useStoredVirtualScroll(containerRef, onScroll, scrollStorageKey)

  useEffect(() => {
    if (range.end + 20 >= rows.length && loadedCount < totalCount) {
      onNeedMore()
    }
  }, [loadedCount, onNeedMore, range.end, rows.length, totalCount])

  // 窗口上报（供容器按可见实例批量取行内指标）：range 变化即通知，页面据此切片并取数。
  useEffect(() => {
    onVisibleRangeChange?.(range.start, range.end)
  }, [onVisibleRangeChange, range.start, range.end])

  return (
    /*
      虚拟化外壳：**只换外观，不碰几何**。
      Card 自带 `flex flex-col gap-4 py-4`，其中 `gap`/`py` 会改变滚动容器的可滚动内容高度、
      进而影响「视口高 → 可视行数」的推算，故这里用 `block`、`gap-0`、`py-0` 逐一还原成
      「一个纯滚动块」：ref 仍挂在同一个 div 上、`flex-1` + `min-h-0` + `overflow-auto` 原样保留，
      行高模型（逐行 sizes / 组头常量 / 占位行行内 style）完全不动。
    */
    <Card
      ref={containerRef}
      onScroll={handleScroll}
      data-testid="instances-table-virtual"
      data-total-count={totalCount}
      /*
            高度策略：`flex-1` 吃掉父级剩余高度，而不是 `max-h-[calc(100vh-20rem)]` 猜出来的定值。

            原写法按「视口高度减去 20rem」估算——20rem 是当时页面其他部分的高度。
            但页面顶部多了 KPI 卡、工具栏改成卡片、间距也调过之后，这个常量就失准了：
            实测表格容器 bottom 落在 1002px、视口只有 900px，**整块列表溢出屏幕 102px**，
            用户必须滚动整页才能看到表格底部，而表格自己又内滚——两层滚动叠加，
            这正是「看起来草台」的直接来源之一。

            `flex-1` + `min-h-0` 让浏览器自己算：父链是 flex 列，剩余空间由布局决定，
            以后再加工具栏或调整间距都不会把表格顶出视口。
          */
      className="block min-h-0 flex-1 gap-0 overflow-auto py-0"
    >
      <Table>
        {header}
        <TableBody>
          {range.before > 0 && (
            // 窗口上占位（高度来自逐行 sizes 的前缀和）；testid 供「内容总高 = 模型总高」断言取数。
            <TableRow aria-hidden="true" data-testid="instances-virtual-spacer-before">
              <TableCell colSpan={columnCount} className="p-0" style={{ height: range.before }} />
            </TableRow>
          )}
          {rows.slice(range.start, range.end).map((row) => row.kind === 'group' ? (
            <TableRow
              key={row.key}
              data-testid="instances-group-row"
              data-group-depth={row.depth}
              data-collapsed={row.collapsed ? 'true' : 'false'}
              // 组头行高写进行盒（不再用单元格的 `h-11`）：声明值与逐行 sizes 是同一个常量。
              style={{ height: groupRowHeight(row.depth) }}
              /*
                【组头不再用整块灰底】原来是 `bg-muted/80 + backdrop-blur`：一整行 44px 的灰条，
                视觉上比表头还重，把「组」压成了「分隔条」，反而看不出它在承载一棵子树。
                改成：底色只到 `bg-muted/40`（比表头更淡），下缘一条主色发丝线作为段的分界，
                让组头读起来像「这一段的标题」而不是「一条横杠」。
              */
              className="sticky top-9 z-10 border-b border-primary/15 bg-muted/40 backdrop-blur"
            >
              <TableCell colSpan={columnCount} className="px-4 py-1.5">
                <InstanceGroupHeaderContent
                  label={row.label}
                  depth={row.depth}
                  count={row.count}
                  health={row.health}
                  collapsed={row.collapsed}
                  onToggle={() => onToggleCollapse(row.collapseKey)}
                />
              </TableCell>
            </TableRow>
          ) : (
            <Fragment key={row.key}>{renderRow(row.instance)}</Fragment>
          ))}
          {range.after > 0 && (
            // 窗口下占位（同上）：展开行的高度必须同样体现在这里，否则滚动条长度与位置映射会漂移。
            <TableRow aria-hidden="true" data-testid="instances-virtual-spacer-after">
              <TableCell colSpan={columnCount} className="p-0" style={{ height: range.after }} />
            </TableRow>
          )}
          {rows.length === 0 && (
            <TableRow>
              <TableCell colSpan={columnCount} className="text-center text-muted-foreground">
                {emptyLabel}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </Card>
  )
}

/**
 * 平铺实例虚拟表（`?groupBy=none`）：单一行模型（只有实例行，含未加载尾部占位）。
 *
 * 行高同样逐行建模——展开行按展开行常量计入，不与行渲染分叉。
 */
export function VirtualizedInstanceTable({
  instances,
  totalCount,
  onNeedMore,
  scrollStorageKey,
  header,
  renderRow,
  emptyLabel,
  isRowExpanded,
  onVisibleRangeChange,
  columnCount = 11,
}: {
  instances: InstanceInfo[]
  totalCount: number
  onNeedMore: () => void
  scrollStorageKey: string
  header: React.ReactNode
  renderRow: (inst: InstanceInfo) => React.ReactNode
  emptyLabel: string
  /** 展开行判据（与行渲染同源）：展开行多渲染一个 `<tr>`，须按展开行高计入模型。 */
  isRowExpanded?: (inst: InstanceInfo) => boolean
  /** 可见窗口上报（range 变化即通知）：容器据此切片实例列表批量取行内指标。 */
  onVisibleRangeChange?: (start: number, end: number) => void
  /** 当前表格总列数（列设置会改变它；占位/空态的 colSpan 必须同步）。缺省 11。 */
  columnCount?: number
}) {
  // 未加载的尾部（index ≥ instances.length）按实例行常量占位，与占位行盒一致。
  const heightAt = useCallback(
    (index: number) => {
      const inst = instances[index]
      return instanceRowHeight(inst !== undefined && isRowExpanded?.(inst) === true)
    },
    [instances, isRowExpanded],
  )
  const sizes = useInstanceRowsSizes(totalCount, heightAt)
  const {
    containerRef,
    onScroll,
    range,
  } = useVirtualRows({
    total: totalCount,
    itemSize: INSTANCE_ROW_HEIGHT,
    sizes,
    overscan: 10,
    fallbackViewportSize: 520,
  })
  const handleScroll = useStoredVirtualScroll(containerRef, onScroll, scrollStorageKey)

  useEffect(() => {
    if (range.end + 20 >= instances.length && instances.length < totalCount) {
      onNeedMore()
    }
  }, [instances.length, onNeedMore, range.end, totalCount])

  // 窗口上报（供容器按可见实例批量取行内指标）：range 变化即通知，页面据此切片并取数。
  useEffect(() => {
    onVisibleRangeChange?.(range.start, range.end)
  }, [onVisibleRangeChange, range.start, range.end])

  const visible = []
  for (let index = range.start; index < range.end; index++) {
    visible.push({ index, inst: instances[index] })
  }

  return (
    /* 外壳同 VirtualizedGroupedInstanceTable：只换外观不碰几何（block / gap-0 / py-0 还原纯滚动块）。 */
    <Card
      ref={containerRef}
      onScroll={handleScroll}
      data-testid="instances-table-virtual"
      data-total-count={totalCount}
      /*
            高度策略：`flex-1` 吃掉父级剩余高度，而不是 `max-h-[calc(100vh-20rem)]` 猜出来的定值。

            原写法按「视口高度减去 20rem」估算——20rem 是当时页面其他部分的高度。
            但页面顶部多了 KPI 卡、工具栏改成卡片、间距也调过之后，这个常量就失准了：
            实测表格容器 bottom 落在 1002px、视口只有 900px，**整块列表溢出屏幕 102px**，
            用户必须滚动整页才能看到表格底部，而表格自己又内滚——两层滚动叠加，
            这正是「看起来草台」的直接来源之一。

            `flex-1` + `min-h-0` 让浏览器自己算：父链是 flex 列，剩余空间由布局决定，
            以后再加工具栏或调整间距都不会把表格顶出视口。
          */
      className="block min-h-0 flex-1 gap-0 overflow-auto py-0"
    >
      <Table>
        {header}
        <TableBody>
          {range.before > 0 && (
            // 窗口上占位（高度来自逐行 sizes 的前缀和）；testid 供「内容总高 = 模型总高」断言取数。
            <TableRow aria-hidden="true" data-testid="instances-virtual-spacer-before">
              <TableCell colSpan={columnCount} className="p-0" style={{ height: range.before }} />
            </TableRow>
          )}
          {visible.map(({ index, inst }) => (
            <Fragment key={inst?.id ?? `placeholder-${index}`}>
              {inst ? renderRow(inst) : (
                <TableRow aria-hidden="true" data-testid="instances-row-placeholder">
                  {/* 未加载行的占位：高度与 INSTANCE_ROW_HEIGHT 同源（模型也按它算该行）。 */}
                  <TableCell colSpan={columnCount} className="p-0" style={{ height: INSTANCE_ROW_HEIGHT }} />
                </TableRow>
              )}
            </Fragment>
          ))}
          {range.after > 0 && (
            // 窗口下占位（同上）：展开行的高度必须同样体现在这里，否则滚动条长度与位置映射会漂移。
            <TableRow aria-hidden="true" data-testid="instances-virtual-spacer-after">
              <TableCell colSpan={columnCount} className="p-0" style={{ height: range.after }} />
            </TableRow>
          )}
          {totalCount === 0 && (
            <TableRow>
              <TableCell colSpan={columnCount} className="text-center text-muted-foreground">
                {emptyLabel}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </Card>
  )
}

export function useStoredVirtualScroll(
  containerRef: React.RefObject<HTMLDivElement | null>,
  onScroll: () => void,
  storageKey: string,
) {
  const clearZeroTimerRef = useRef<number | null>(null)

  const cancelClearZeroTimer = useCallback(() => {
    if (clearZeroTimerRef.current == null) return
    window.clearTimeout(clearZeroTimerRef.current)
    clearZeroTimerRef.current = null
  }, [])

  useLayoutEffect(() => {
    const saved = Number(sessionStorage.getItem(storageKey) ?? 0)
    if (!Number.isFinite(saved) || saved <= 0) return

    let cancelled = false
    let attempts = 0
    let timer: number | null = null
    const restore = () => {
      if (cancelled) return
      const el = containerRef.current
      attempts += 1
      if (!el) {
        if (attempts < 20) timer = window.setTimeout(restore, 50)
        return
      }
      el.scrollTop = saved
      onScroll()
      if (el.scrollTop < saved && attempts < 20) timer = window.setTimeout(restore, 50)
    }

    timer = window.setTimeout(restore, 0)
    return () => {
      cancelled = true
      if (timer != null) window.clearTimeout(timer)
    }
  }, [containerRef, onScroll, storageKey])

  useEffect(() => cancelClearZeroTimer, [cancelClearZeroTimer])

  return useCallback(() => {
    onScroll()
    const el = containerRef.current
    if (!el) return

    const scrollTop = Math.round(el.scrollTop)
    if (scrollTop <= 0) {
      cancelClearZeroTimer()
      clearZeroTimerRef.current = window.setTimeout(() => {
        const currentKey = `${SCROLL_KEY_PREFIX}${window.location.pathname}${window.location.search}`
        if (currentKey === storageKey) sessionStorage.removeItem(storageKey)
        clearZeroTimerRef.current = null
      }, 150)
      return
    }

    cancelClearZeroTimer()
    sessionStorage.setItem(storageKey, String(scrollTop))
  }, [cancelClearZeroTimer, containerRef, onScroll, storageKey])
}
