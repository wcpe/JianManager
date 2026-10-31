import { Table, TableBody, TableCell, TableRow } from '@jianmanager/ui/components/table'
import { Fragment, useRef, type ReactNode } from 'react'

import type { InstanceInfo } from '@/api/instances'
import {
  useStoredVirtualScroll,
  type InstanceTreeRow,
} from '@/components/views/instances/VirtualizedInstanceTables'
import { computeVisibleColumns, DEFAULT_COLUMN_WIDTHS, type InstanceColumnKey, type InstanceColumnWidths } from '@/lib/instances/instance-view-prefs'
import { InstancePaginationBar } from '@/components/views/instances/InstancePaginationBar'

/**
 * 各列宽度的取值：用户拖拽过的宽度优先，否则用默认宽度。
 * 名称列默认不设宽度（吃掉剩余空间），用户拖过之后才变成固定像素。
 */
function resolveColumnWidth(
  key: InstanceColumnKey,
  widths: InstanceColumnWidths | undefined,
): number | undefined {
  return widths?.[key] ?? DEFAULT_COLUMN_WIDTHS[key]
}

/** 
 * 标准分页表格（服务器页「列表视图」外壳）。
 *
 * 【为什么换掉虚拟滚动】原实现用自定义 div 容器 + 虚拟窗口模拟表格：
 * 结构上虽复用了 `TableRow/TableCell`，但外壳不是 `Table`，行高被钉成固定像素，
 * 滚动容器自带一套 padding 与占位逻辑。对「运营要在 1200 条里找人」这件事，
 * 标准分页表格（列头吸顶、每页 50 条、页码可跳）比一次滞千条更符合直觉，
 * 也与其他页（用户 / 备份）保持同一形态。
 *
 * 【保留了什么】行内容仍由 `renderRow` 注入（`InstanceRowView` 不变），
 * 表头仍由 `InstanceTableHeader` 注入，列设置/排序/多选的既有行为全部沿用。
 */
export function PaginatedInstanceTable({
  instances,
  total,
  page,
  pageSize,
  isFetching,
  onPageChange,
  header,
  renderRow,
  emptyLabel,
  treeRows,
  renderGroupRow,
  visibleColumns,
  columnOrder,
  columnWidths,
  scrollStorageKey,
}: {
  instances: InstanceInfo[]
  /** 服务端总数（分页页数由它算）。 */
  total: number
  /** 当前页（1-based）。 */
  page: number
  pageSize: number
  isFetching: boolean
  onPageChange: (page: number) => void
  header: ReactNode
  renderRow: (inst: InstanceInfo) => ReactNode
  emptyLabel: string
  columnCount?: number
  /** 
   * 分组视图的行序列（组头 + 实例混排）。给了就按它渲染，否则直接铺 `instances`。
   * 两者只取其一：分组视图下 `instances` 只是树里的一部分，直接铺会丢掉组头与层级。
   */
  treeRows?: InstanceTreeRow[]
  /** 组头行渲染（由调用方注入，复用既有 `InstanceGroupHeaderContent`）。 */
  renderGroupRow?: (row: Extract<InstanceTreeRow, { kind: 'group' }>) => ReactNode
  /** 列顺序（表头拖拽换位结果）。与表头/行渲染共用同一份，缺省为默认顺序。 */
  columnOrder?: readonly InstanceColumnKey[]
  /** 用户拖拽过的列宽（缺省列走 DEFAULT_COLUMN_WIDTHS）。 */
  columnWidths?: InstanceColumnWidths
  /** 可见的可选列（决定 colgroup 的列数与宽度，必须与表头/行渲染同源）。 */
  visibleColumns?: ReadonlySet<string>
  /**
   * 滚动位置存储键（`jm.instances.scroll:<path><search>`）。
   *
   * 【为何必须传】换掉虚拟滚动后漏了这一项 → 列表视图「滚到什么位置、点进详情、后退回来」
   * 不再回到原位置（卡片视图一直有，两个视图行为就不一致了）。
   * 复用虚拟表同款的 `useStoredVirtualScroll`：容器换成普通分页表并不影响
   * 「记住容器 scrollTop 并在下次挂载时恢复」这件事，滚动容器标识仍由同一个
   * `instances-table-virtual` 承载。
   */
  scrollStorageKey?: string
}) {
  const scrollRef = useRef<HTMLDivElement | null>(null)
  // 滚动位置写入/恢复：与虚拟表、卡片视图同一实现（细节见 useStoredVirtualScroll）。
  // 未传 key 时退化为空串 —— sessionStorage 键为空串不会撞上任何真实条目。
  const handleScroll = useStoredVirtualScroll(scrollRef, () => {}, scrollStorageKey ?? '')
  /* 
    colgroup 的列顺序：与表头/行共用同一个计算函数（见 computeVisibleColumns 的注释）。
    此前这里自己写了一份 present 判断，新增 business 列时漏加，导致 colgroup 少一个 <col>、
    整表列宽错位——这类「三处各算一遍」的漂移已经从结构上消除。
  */
  const orderedColumnKeys = computeVisibleColumns(columnOrder, visibleColumns)

  return (
    <div className="flex min-h-0 flex-col gap-2">
      {/*
        data-testid 沿用虚拟表的 `instances-table-virtual`：它表达的是「实例主表滚动区」这一语义，
        与实现是虚拟滚动还是服务端分页无关。测试与 e2e 依赖该锚点定位表体，
        换成实现专属的名字会让既有断言全部失效（且并未带来更好的表达力）。

        `data-total-count` 同样必须保留（值为服务端总数）：虚拟表会写这个属性供
        虚拟化断言取数（见 VirtualizedInstanceTables），而本组件换成服务端分页后
        没写它 → `Number(attr ?? '0')` 恒为 0，于是「1200 条数据」的断言全部报
        `Expected >= 1000, Received: 0`。属性语义是「本视图对应的服务端总量」，
        与渲染方式无关，两种实现都必须给。
      */}
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="min-h-0 flex-1 overflow-auto rounded-md border"
        data-testid="instances-table-virtual"
        data-total-count={total}
      >
        {/* overflow-auto 双向：列多时横向滚动，避免「状态/操作」列被裁在视口外。 */}
        {/* 不设 min-w：各列本身已是 nowrap 紧凑内容，让表格按可用宽度自适应；
            真放不下时 Table 自带的 overflow-x-auto 会接管横向滚动，不会裁掉右侧列。 */}
        {/*
          固定列宽（table-fixed + colgroup）。**必须固定**：自动布局会按每行内容重算列宽，
          崩溃行的徽章/标签更宽，于是它整行的列边界与普通行错开（实测左移 24px）。
          colgroup 的 `<col>` 数量与顺序必须与表头/行渲染**完全同源**（都用 visibleColumns 判断），
          否则列数不匹配时错位更严重。
        */}
        <Table className="table-fixed">
          {/*
            colgroup 由 `columnOrder` 驱动，与表头/行渲染**同一份顺序**：
            三处只要有一处不同源就会整表错列（先后踩过两次——状态/健康度写反、
            以及健康度格未跟随列设置）。
          */}
          <colgroup>
            {orderedColumnKeys.map((key) => {
              const width = resolveColumnWidth(key, columnWidths)
              return <col key={key} style={width ? { width } : undefined} />
            })}
          </colgroup>
          {/* header 由调用方注入，**它本身就是 <TableHeader>**（InstanceTableHeader 的返回值）。
              这里不能再包一层：thead 嵌套 thead 是非法 HTML，浏览器会静默丢弃表格结构，
              表现为「表格整个不渲染」而不是报错。 */}
          {header}
          <TableBody>
            {/*
              【空态优先于分组树】此前写成 `treeRows ? treeRows.map(...) : 空态`，而 `[]` 是 truthy：
              分组视图（含默认视图）下空结果/查询失败时只渲染空表体，用户看不到任何「无匹配」提示。
              现在先判空再分流，两种视图共用同一空态。
            */}
            {(treeRows ? treeRows.length === 0 && instances.length === 0 : instances.length === 0) ? (
              <TableRow>
                <TableCell colSpan={orderedColumnKeys.length} className="py-10 text-center text-sm text-muted-foreground">
                  {emptyLabel}
                </TableCell>
              </TableRow>
            ) : treeRows ? (
              treeRows.map((row) =>
                row.kind === 'group'
                  ? renderGroupRow?.(row)
                  : <Fragment key={row.key}>{renderRow(row.instance)}</Fragment>,
              )
            ) : (
              instances.map((inst) => <Fragment key={inst.id}>{renderRow(inst)}</Fragment>)
            )}
          </TableBody>
        </Table>
      </div>

      {/* 分页条：与卡片视图共用同一组件（见 InstancePaginationBar 的注释）。 */}
      <InstancePaginationBar
        loaded={instances.length}
        total={total}
        page={page}
        pageSize={pageSize}
        isFetching={isFetching}
        onPageChange={onPageChange}
      />
    </div>
  )
}
