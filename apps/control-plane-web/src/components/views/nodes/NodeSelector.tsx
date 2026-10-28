import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, ChevronDown, Search, Server, ServerOff, Wrench } from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import type { NodeInfo } from '@/lib/nodes/node-types'

/**
 * 节点选择器（下拉）：替代原来的左侧节点列表。
 *
 * 【为什么改成下拉】原左栏是「列表页的弱化副本」——列表页已有完整的筛/排/分页，
 * 左栏只有名字和状态点，两处都能看节点列表，功能重叠（用户指出）。云厂商控制台的做法是
 * **顶部一个地域/实例选择器 + 内容占满宽度**：切换目标用选择器，看内容用大屏。
 * 现在左栏空间还给内容区（从 ~288px 变成全宽），切换仍然只需两次点击。
 *
 * 【为什么自带搜索】36~100 台时针点滚动找名字很慢；搜索是这一层唯一必需的筛选能力
 * （其余筛选统一回列表页做，避免又一次能力重复）。
 *
 * 【分组依据】按状态分组（离线 / 维护中 / 在线）：这三类的处理优先级不同，
 * 把「需要处理的」排在前面，比按字母序更有用。
 */
export function NodeSelector({
  nodes,
  selectedId,
  onSelect,
  className,
}: {
  nodes: NodeInfo[]
  selectedId: number | null
  onSelect: (id: number) => void
  className?: string
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')

  const selected = nodes.find((n) => n.id === selectedId) ?? null

  const groups = useMemo(() => {
    const q = query.trim().toLowerCase()
    const match = (n: NodeInfo) =>
      q === '' || n.name.toLowerCase().includes(q) || n.host.toLowerCase().includes(q)
    const pool = nodes.filter(match)
    return [
      { key: 'offline', label: t('nodes.offline'), items: pool.filter((n) => n.status === 0), icon: ServerOff },
      {
        key: 'maintenance',
        label: t('nodes.maintenance'),
        items: pool.filter((n) => n.status !== 0 && n.maintenance),
        icon: Wrench,
      },
      {
        key: 'online',
        label: t('nodes.online'),
        items: pool.filter((n) => n.status === 1 && !n.maintenance),
        icon: Server,
      },
    ].filter((g) => g.items.length > 0)
  }, [nodes, query, t])

  if (!selected) return null

  return (
    <div className={cn('relative', className)}>
      {/* 触发按钮：显示当前节点 + 状态点，点击展开选择面板。 */}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={t('nodes.selectNode', '选择节点')}
        className="h-8 max-w-[280px] gap-1.5"
      >
        <span
          className={cn(
            'size-2 shrink-0 rounded-full',
            selected.status === 1
              ? selected.maintenance
                ? 'bg-status-warning'
                : 'bg-status-success'
              : 'bg-status-danger',
          )}
          aria-hidden
        />
        <span className="truncate font-medium">{selected.name}</span>
        <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">{selected.host}</span>
        <ChevronDown className={cn('size-3.5 shrink-0 transition-transform', open && 'rotate-180')} aria-hidden />
      </Button>

      {open && (
        <>
          {/* 点击外部关闭：用一层透明遮罩承接，避免给 document 挂监听。 */}
          <div className="fixed inset-0 z-40" onClick={() => setOpen(false)} aria-hidden />
          <div
            role="listbox"
            aria-label={t('nodes.selectNode', '选择节点')}
            data-testid="node-selector-panel"
            className="absolute left-0 top-full z-50 mt-1 flex max-h-[min(70vh,460px)] w-80 flex-col overflow-hidden rounded-md border bg-popover shadow-lg"
          >
            <div className="relative border-b p-2">
              <Search className="pointer-events-none absolute left-4 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                autoFocus
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder={t('nodes.searchPlaceholder')}
                aria-label={t('nodes.searchPlaceholder')}
                className="h-8 pl-8 text-xs"
              />
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto p-1">
              {groups.length === 0 ? (
                <p className="px-2 py-4 text-center text-xs text-muted-foreground">{t('nodes.searchEmpty')}</p>
              ) : (
                groups.map((group) => (
                  <div key={group.key} className="mb-1 last:mb-0">
                    {/* 组头：状态 + 数量。离线排最前——需要处理的先看到。 */}
                    <div className="flex items-center gap-1 px-2 py-1 text-[10px] font-semibold uppercase tracking-wide text-muted-foreground/70">
                      <group.icon className="size-3" aria-hidden />
                      {group.label}
                      <span className="tabular-nums">{group.items.length}</span>
                    </div>
                    {group.items.map((n) => {
                      const active = n.id === selectedId
                      return (
                        <button
                          key={n.id}
                          type="button"
                          role="option"
                          aria-selected={active}
                          data-testid="node-selector-option"
                          onClick={() => {
                            onSelect(n.id)
                            setOpen(false)
                            setQuery('')
                          }}
                          className={cn(
                            'flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs outline-none',
                            'hover:bg-accent focus-visible:bg-accent',
                            active && 'bg-accent',
                          )}
                        >
                          <span
                            className={cn(
                              'size-1.5 shrink-0 rounded-full',
                              n.status === 1
                                ? n.maintenance
                                  ? 'bg-status-warning'
                                  : 'bg-status-success'
                                : 'bg-status-danger',
                            )}
                            aria-hidden
                          />
                          <span className="min-w-0 flex-1 truncate font-medium">{n.name}</span>
                          <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">{n.host}</span>
                          {active && <Check className="size-3.5 shrink-0 text-primary" aria-hidden />}
                        </button>
                      )
                    })}
                  </div>
                ))
              )}
            </div>
          </div>
        </>
      )}
    </div>
  )
}
