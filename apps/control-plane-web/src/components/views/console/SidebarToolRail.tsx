/**
 * @file SidebarToolRail：侧栏底部工具区（重构件修订）——**横排图标+文字行** + 「更多」收纳菜单。
 *
 * 工具集合来自 `workspace-navigation.ts` 的 ops「群组与工作台」组（真源，权限裁剪后注入）；
 * 收纳偏好存于 `lib/console/sidebar-tools`（`jm.sidebar.pinnedTools`，useSyncExternalStore）。
 *
 * 形态（用户指定）：
 * - **显示在侧边栏**：每个工具一行（图标 + 文字，28px 紧凑行），位于侧栏主体，可直接点击跳转；
 * - **已收纳**：不在底部显示任何图标——底部只剩主题/语言与「更多」，菜单里可把它们拖回来；
 * - 「更多」菜单两区（显示在侧边栏 / 已收纳），**支持拖拽排序与跨区拖动**（参照用户提供的菜单形态）：
 *   拖到「显示在侧边栏」区即置回、拖到「已收纳」区即收纳；同区内拖动改变顺序。
 *
 * 【为什么不用 HTML5 原生拖拽】菜单是浮层、条目数量少且始终挂载（不虚拟化），
 * 用 pointer 事件自绘拖拽可以精确控制插入位置与落点高亮，也不必依赖 dataTransfer 序列化。
 * 若将来条目数增长到需要虚拟化，必须改为把拖拽源钉在渲染集合里（护栏要求）。
 */
import { useCallback, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { GripVertical, MoreHorizontal } from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import { Separator } from '@jianmanager/ui/components/separator'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'
import type { NavEntry } from '@/lib/shared/navigation-types'
import { setPinnedTools, usePinnedTools } from '@/lib/console/sidebar-tools'
import type { SidebarLinkArgs } from '@/components/views/console/sidebar-link'

/**
 * 「显示在侧边栏」的工具行（一行一个：图标 + 文字）。
 *
 * 位置在底部偏好块**上方**、资源区下方——它属于「底部常驻」的一部分，
 * 但不与主题/语言/更多挤同一行（工具是导航，与偏好控件不是一类）。
 * 一个都没置回时整段不渲染（含其分隔线），避免底部出现空块。
 */
export function SidebarPinnedTools({
  entries,
  pathname,
  renderLink,
  collapsed,
}: SidebarToolRailProps & { collapsed: boolean }) {
  const { t } = useTranslation()
  const pinned = usePinnedTools()
  const shown = pinned
    .map((to) => entries.find((entry) => entry.to === to))
    .filter((entry): entry is NavEntry => entry !== undefined)
  if (shown.length === 0) return null

  return (
    <div data-slot="sidebar-pinned-tools" className="shrink-0 px-[9px] pb-[2px]">
      {/*
        【这里不画分隔线】用户反馈「底下的按钮栏上面有个多余的分割线」，指的就是这一条：
        工具段自己在顶部画了 `border-t`，而外层 `SideNavBottom` **也**有一条 `border-t`，
        两者相隔仅几像素——视觉上就是两条线叠在底部按钮栏上方。
        保留外层的（它才是「底部区」的边界），工具段不再自己画。
      */}
      <div>
        {shown.map((entry) => (
          <div key={entry.to} className="flex h-[26px] items-center">
            {renderLink({
              to: entry.to,
              // 与资源区行同款：真 `<a href>`，中键/新标签页可用（护栏要求语义落在可聚焦元素上）。
              ariaCurrent: pathname === entry.to || pathname.startsWith(`${entry.to}/`) ? 'page' : undefined,
              className: (active: boolean) =>
                `flex min-w-0 flex-1 items-center gap-[7px] rounded-[var(--radius)] px-[6px] py-[3px] text-[11px] transition-colors ${
                  active ? 'bg-accent/60 text-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground'
                }`,
              children: (
                <>
                  {entry.icon ? <entry.icon className="size-3.5 shrink-0" /> : null}
                  {!collapsed && <span className="min-w-0 flex-1 truncate">{t(entry.labelKey)}</span>}
                </>
              ),
            })}
          </div>
        ))}
      </div>
    </div>
  )
}

export interface SidebarToolRailProps {
  /** 可收纳工具（登记真源，权限裁剪后）。 */
  entries: NavEntry[]
  pathname: string
  renderLink: (args: SidebarLinkArgs) => ReactNode
}

/**
 * 底部「更多」入口（收纳菜单）。
 *
 * 工具行本身不在这里：它们在侧栏主体由 `SidebarPinnedTools` 渲染（一行一个、图标+文字）。
 * 底部只保留这一个入口——已收纳的工具在底部不占位，需要时从菜单里拖回来。
 */
export function SidebarToolRail({ entries }: SidebarToolRailProps) {
  return (
    // 不再包一层左侧留白：容器已由 `SidebarPreferences` 摆好位置，这里只需靠右对齐的三个点。
    <div data-slot="sidebar-tool-rail" className="flex justify-end">
      <ToolMoreMenu entries={entries} />
    </div>
  )
}

/** 拖拽中的条目：来源区 + 当前落点。 */
interface DragState {
  to: string
  /** 拖到哪一区（`shown`=显示在侧边栏 / `stowed`=已收纳）。 */
  zone: 'shown' | 'stowed'
  /** 落点序号（插入位置）。 */
  index: number
}

/**
 * 「更多」收纳菜单：两区 + 拖拽排序 + 跨区拖动。
 *
 * 落点计算用「指针 y 坐标 vs 每行中线」，同区拖动时插入位置会跳过自身，
 * 避免拖到自己身上产生抖动。写入是一次 `setPinnedTools` 整表替换，天然幂等。
 */
function ToolMoreMenu({ entries }: { entries: NavEntry[] }) {
  const { t } = useTranslation()
  const pinned = usePinnedTools()
  const shownEntries = pinned
    .map((to) => entries.find((entry) => entry.to === to))
    .filter((entry): entry is NavEntry => entry !== undefined)
  const stowedEntries = entries.filter((entry) => !pinned.includes(entry.to))

  const [drag, setDrag] = useState<DragState | null>(null)
  const listRefs = useRef(new Map<string, HTMLDivElement>())
  const menuRef = useRef<HTMLDivElement | null>(null)

  /** 指针落在哪一行、该插到第几个。 */
  const resolveDrop = useCallback((clientY: number, zone: 'shown' | 'stowed'): number => {
    const rows = [...listRefs.current.entries()]
      .filter(([key]) => key.startsWith(`${zone}:`))
      .sort((a, b) => a[1].getBoundingClientRect().top - b[1].getBoundingClientRect().top)
    for (const [, element] of rows) {
      const rect = element.getBoundingClientRect()
      if (clientY < rect.top + rect.height / 2) {
        return Number(element.dataset.index ?? 0)
      }
    }
    return rows.length
  }, [])

  const commit = useCallback((target: DragState) => {
    const stowedOrder = entries.map((entry) => entry.to)
    const current = pinned
    const without = current.filter((to) => to !== target.to)
    // 目标区内的基准顺序：显示区用当前 pinned 顺序，收纳区用登记顺序去除已置回项。
    // 收纳区只是「不在 pinned 里」；拖动收纳 = 从 pinned 移除，顺序沿用登记序。
    // 显示区 = 在 pinned 里按落点插入，顺序即显示顺序。
    const insertAt = Math.min(target.index, without.length)
    const next = target.zone === 'shown'
      ? [...without.slice(0, insertAt), target.to, ...without.slice(insertAt)]
      : without
    setPinnedTools(next)
    void stowedOrder
  }, [entries, pinned])

  const row = (entry: NavEntry, zone: 'shown' | 'stowed', index: number) => {
    const Icon = entry.icon
    const label = t(entry.labelKey)
    const dragging = drag?.to === entry.to
    return (
      <div
        key={entry.to}
        ref={(element) => {
          if (element) listRefs.current.set(`${zone}:${entry.to}`, element)
          else listRefs.current.delete(`${zone}:${entry.to}`)
        }}
        data-index={index}
        data-testid={`sidebar-tool-row-${zone}-${entry.to}`}
        onPointerDown={(event) => {
          // 只在左键按下时进入拖拽；否则保留菜单自身的键盘/右键行为。
          if (event.button !== 0) return
          setDrag({ to: entry.to, zone, index })
          event.currentTarget.setPointerCapture(event.pointerId)
        }}
        onPointerUp={(event) => {
          if (!drag) return
          if (event.currentTarget.hasPointerCapture(event.pointerId)) {
            event.currentTarget.releasePointerCapture(event.pointerId)
          }
          if (drag.to === entry.to) commit(drag)
          setDrag(null)
        }}
        onPointerCancel={() => setDrag(null)}
        className={cn(
          'flex cursor-grab items-center gap-1 rounded-sm px-1.5 py-1 text-xs transition-colors hover:bg-accent/60',
          dragging && 'opacity-50',
        )}
      >
        <GripVertical className="size-3 shrink-0 text-muted-foreground/40" aria-hidden="true" />
        <span className="flex size-4 shrink-0 items-center justify-center text-muted-foreground">
          {Icon ? <Icon className="size-3.5" /> : null}
        </span>
        <span className="min-w-0 flex-1 truncate">{label}</span>
      </div>
    )
  }

  const zone = (key: 'shown' | 'stowed', title: string, rows: NavEntry[]) => (
    <div
      data-slot={`sidebar-tool-zone-${key}`}
      className={cn('rounded-md', drag?.zone === key && 'bg-accent/30')}
    >
      <div className="px-2 py-1.5 text-xs font-medium text-muted-foreground">{title}</div>
      {rows.length === 0
        ? <div className="px-2 pb-1.5 text-[11px] text-muted-foreground/60">{t('sidebarTools.emptyZone')}</div>
        : rows.map((entry, index) => row(entry, key, index))}
    </div>
  )

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        {/*
          用户指定：**纯三个点小图标**，不写字。位置由 `SidebarPreferences.moreMenu` 决定——
          在「版本号 / 开源协议」那一行的正上方。`aria-label` 落在真按钮上（护栏要求）。
          22px 的小图标位由 className 定尺寸，故取 invisible+auto 不叠按钮盒。
        */}
        <Button
          type="button"
          variant="invisible"
          size="auto"
          aria-label={t('sidebarTools.more')}
          title={t('sidebarTools.more')}
          data-testid="sidebar-tools-more"
          className="ml-auto flex size-[22px] items-center justify-center rounded-md text-muted-foreground/70 transition-colors hover:bg-muted hover:text-foreground"
        >
          <MoreHorizontal className="size-[15px]" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" className="w-60">
        <div
          ref={menuRef}
          onPointerMove={(event) => {
            // 【必须在菜单容器上一层判定】行的 `setPointerCapture` 会把后续 move/up 事件
            // 全部锁给源行，区容器根本收不到——跨区拖拽因此永远失败（实测 pins=[] 的成因）。
            // 收敛到菜单容器后，指针落在哪个区、插到第几位都在同一处算。
            if (!drag) return
            const menu = menuRef.current
            if (!menu) return
            const bounds = menu.getBoundingClientRect()
            if (event.clientY < bounds.top || event.clientY > bounds.bottom) return
            const shownRect = menu.querySelector('[data-slot="sidebar-tool-zone-shown"]')?.getBoundingClientRect()
            const inShown = shownRect ? event.clientY < shownRect.bottom : true
            const nextZone: 'shown' | 'stowed' = inShown ? 'shown' : 'stowed'
            const nextIndex = resolveDrop(event.clientY, nextZone)
            if (drag.zone !== nextZone || drag.index !== nextIndex) {
              setDrag({ ...drag, zone: nextZone, index: nextIndex })
            }
          }}
        >
        {zone('shown', t('sidebarTools.shown'), shownEntries)}
        {/* 两组工具之间的纯装饰分隔线 */}
        <Separator decorative className="my-1" />
        {zone('stowed', t('sidebarTools.stowed'), stowedEntries)}
        <p className="border-t border-border/60 px-2 py-1.5 text-[10px] leading-4 text-muted-foreground/60">
          {t('sidebarTools.dragHint')}
        </p>
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
