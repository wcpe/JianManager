import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Search } from 'lucide-react'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { useNodePorts } from '@/api/ports'
import { useVirtualRows } from '@/lib/virtual-list'

/** 端口表虚拟化行高（px），与 Table 行内边距匹配。 */
const PORT_ROW_HEIGHT = 44

/** 节点端口占用面板（FR-032）：展示系统已分配的 server/query 端口与分配范围（RCON 已退役 FR-067）。 */
export default function NodePortsPanel({ nodeId }: { nodeId: number }) {
  const { t } = useTranslation()
  const { data, isLoading } = useNodePorts(nodeId)
  // 实例名过滤（大量占用时便于定位某端口，大小写不敏感子串匹配）。
  const [filter, setFilter] = useState('')

  const occupied = useMemo(() => data?.occupied ?? [], [data])
  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return occupied
    return occupied.filter((p) => p.name.toLowerCase().includes(q))
  }, [occupied, filter])

  const { containerRef, onScroll, range } = useVirtualRows({
    total: filtered.length,
    itemSize: PORT_ROW_HEIGHT,
    overscan: 8,
    fallbackViewportSize: 480,
  })

  // 过滤收敛后可视窗口可能越界（如从 100 行过滤到 3 行）；重置滚动到顶避免空窗。
  useEffect(() => {
    const el = containerRef.current
    if (el && el.scrollTop > 0 && range.start >= filtered.length) {
      el.scrollTop = 0
      onScroll()
    }
  }, [filtered.length, range.start, containerRef, onScroll])

  if (isLoading)
    // 骨架占位：范围说明行 + 两行表格行轮廓，替代裸文字避免布局跳动。
    return (
      <div className="space-y-2">
        <Skeleton className="h-4 w-56" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-full" />
      </div>
    )

  const visible = filtered.slice(range.start, range.end)

  return (
    <div>
      {data && (
        <p className="text-xs text-muted-foreground mb-3">
          {t('ports.range', {
            server: data.ranges.serverPortBase,
            size: data.ranges.rangeSize,
          })}
        </p>
      )}
      {/* 实例名过滤（FR-032 增强）：大量端口占用时快速定位。 */}
      <div className="relative mb-2 max-w-xs">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
        <input
          type="search"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          aria-label={t('ports.filterPlaceholder', { defaultValue: 'Filter by instance name' })}
          placeholder={t('ports.filterPlaceholder', { defaultValue: 'Filter by instance name' })}
          className="h-8 w-full rounded-md border bg-background pl-8 pr-2 text-sm outline-none ring-primary/30 focus:ring-2"
        />
      </div>
      <div
        ref={containerRef}
        onScroll={onScroll}
        data-testid="node-ports-virtual"
        className="max-h-[26rem] overflow-auto rounded-md border"
      >
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('ports.instance')}</TableHead>
              <TableHead>{t('ports.role')}</TableHead>
              <TableHead>{t('ports.serverPort')}</TableHead>
              <TableHead>{t('ports.queryPort')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {range.before > 0 && (
              <TableRow aria-hidden="true">
                <TableCell colSpan={4} className="p-0" style={{ height: range.before }} />
              </TableRow>
            )}
            {visible.map((p) => (
              <TableRow key={p.instanceId}>
                <TableCell className="font-medium">{p.name}</TableCell>
                <TableCell>{t(`networks.role_${p.role}`, { defaultValue: p.role })}</TableCell>
                <TableCell>{p.serverPort || '--'}</TableCell>
                <TableCell>{p.queryPort || '--'}</TableCell>
              </TableRow>
            ))}
            {range.after > 0 && (
              <TableRow aria-hidden="true">
                <TableCell colSpan={4} className="p-0" style={{ height: range.after }} />
              </TableRow>
            )}
            {filtered.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="text-center text-muted-foreground">
                  {filter.trim() && occupied.length > 0
                    ? t('ports.noMatch', { defaultValue: 'No matching ports' })
                    : t('ports.empty')}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}
