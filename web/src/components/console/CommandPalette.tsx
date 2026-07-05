import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Box, CornerDownLeft, Network, Search, Server, Terminal } from 'lucide-react'

import { useInstances } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import { cn } from '@jianmanager/ui'
import { instanceStatusLevel } from '@jianmanager/ui'
import { flatNavItems } from './nav-config'
import { searchPalette, type PaletteEntry } from './command-palette'

/** kind → 行首图标。 */
const KIND_ICON = {
  instance: Box,
  node: Server,
  page: Network,
  command: Terminal,
} as const

/**
 * 全局命令面板（FR-241，导航外壳 v2 Part A）：Ctrl/⌘+K 或点页眉搜索框打开，
 * 单输入框检索实例/节点/页面/操作并跳转或执行。键盘 ↑↓ 选择、Enter 执行、Esc 关。
 * 纯检索/匹配逻辑下沉 `command-palette.ts`（vitest 覆盖），本组件负责数据源、动作与交互。
 */
export default function CommandPalette() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const role = useAuthStore((s) => s.role)
  const open = useConsoleStore((s) => s.commandPaletteOpen)
  const setOpen = useConsoleStore((s) => s.setCommandPaletteOpen)
  const openInstance = useConsoleStore((s) => s.openInstance)
  const closeInstance = useConsoleStore((s) => s.closeInstance)
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)
  const setSelectedNodeId = useConsoleStore((s) => s.setSelectedNodeId)

  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  // 实例/节点列表（页眉与侧栏已拉取，此处复用同 queryKey 缓存，不额外加压）。
  const { data: instances } = useInstances()
  const { data: nodes } = useNodes()

  // 操作类目标（静态）：执行副作用而非跳转。
  const commands = useMemo(
    () => [
      { id: 'refresh', label: t('palette.cmdRefresh') },
      { id: 'toggle-sidebar', label: t('palette.cmdToggleSidebar') },
    ],
    [t],
  )

  const pages = useMemo(
    () => flatNavItems(role).map((n) => ({ to: n.to, label: t(n.labelKey) })),
    [role, t],
  )

  const entries = useMemo(
    () =>
      searchPalette(query, {
        instances: (instances ?? []).map((i) => ({ id: i.id, name: i.name, uuid: i.uuid, status: i.status, nodeId: i.nodeId })),
        nodes: (nodes ?? []).map((n) => ({ id: n.id, name: n.name, host: n.host })),
        pages,
        commands,
        nodeScopeId: selectedNodeId,
      }),
    [query, instances, nodes, pages, commands, selectedNodeId],
  )

  // selected 在渲染期 clamp，避免结果变化后越界（不在 effect 里 setState）。
  const activeIndex = entries.length === 0 ? -1 : Math.min(selected, entries.length - 1)

  // 全局 Ctrl/⌘+K 切换（始终挂载的监听；事件回调里 setState 合法，非 effect body）。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setQuery('')
        setSelected(0)
        setOpen(!useConsoleStore.getState().commandPaletteOpen)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [setOpen])

  // 打开时聚焦输入框（DOM 副作用，非 setState）。
  useEffect(() => {
    if (open) inputRef.current?.focus()
  }, [open])

  if (!open) return null

  const close = () => {
    setOpen(false)
    setQuery('')
    setSelected(0)
  }

  const run = (entry: PaletteEntry) => {
    const [kind, rest] = [entry.kind, entry.key.slice(entry.kind.length + 1)]
    if (kind === 'instance') {
      openInstance(Number(rest))
      navigate('/instances')
    } else if (kind === 'node') {
      setSelectedNodeId(Number(rest))
      navigate(`/nodes?node=${rest}`)
      closeInstance()
    } else if (kind === 'page') {
      navigate(rest)
      closeInstance()
    } else if (kind === 'command') {
      if (rest === 'refresh') void queryClient.invalidateQueries()
      else if (rest === 'toggle-sidebar') toggleSidebar()
    }
    close()
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      close()
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setSelected((s) => Math.min(s + 1, entries.length - 1))
      scrollIntoView(listRef.current, activeIndex + 1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setSelected((s) => Math.max(s - 1, 0))
      scrollIntoView(listRef.current, activeIndex - 1)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (activeIndex >= 0) run(entries[activeIndex])
    }
  }

  return (
    <div
      className="fixed inset-0 z-[60] flex items-start justify-center bg-black/50 p-4 pt-[14vh]"
      onClick={close}
      role="presentation"
    >
      <div
        className="flex max-h-[60vh] w-full max-w-xl flex-col overflow-hidden rounded-xl border bg-card text-card-foreground shadow-lift"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal
        aria-label={t('palette.title')}
      >
        <div className="flex items-center gap-2 border-b px-3">
          <Search className="size-4 shrink-0 text-muted-foreground" />
          <input
            ref={inputRef}
            value={query}
            onChange={(e) => {
              setQuery(e.target.value)
              setSelected(0)
            }}
            onKeyDown={onKeyDown}
            placeholder={t('palette.placeholder')}
            aria-label={t('palette.placeholder')}
            className="h-11 flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          />
          <kbd className="hidden shrink-0 rounded border bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground sm:inline-block">
            Esc
          </kbd>
        </div>

        <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto p-1.5">
          {entries.length === 0 ? (
            <p className="px-3 py-6 text-center text-sm text-muted-foreground">{t('palette.empty')}</p>
          ) : (
            entries.map((entry, i) => {
              const Icon = KIND_ICON[entry.kind]
              const active = i === activeIndex
              return (
                <button
                  key={entry.key}
                  type="button"
                  data-palette-row={i}
                  onMouseMove={() => setSelected(i)}
                  onClick={() => run(entry)}
                  className={cn(
                    'flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm transition-colors',
                    active ? 'bg-accent text-foreground' : 'text-foreground/80',
                  )}
                >
                  {entry.kind === 'instance' ? (
                    <span
                      className="size-2 shrink-0 rounded-full"
                      style={{ backgroundColor: `var(--status-${statusColor(entry.status)})` }}
                      aria-hidden
                    />
                  ) : (
                    <Icon className="size-4 shrink-0 text-muted-foreground" />
                  )}
                  <span className="min-w-0 flex-1 truncate">{entry.label}</span>
                  {entry.sublabel && (
                    <span className="shrink-0 truncate text-[11px] text-muted-foreground">{entry.sublabel}</span>
                  )}
                  <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                    {t(`palette.kind.${entry.kind}`)}
                  </span>
                  {active && <CornerDownLeft className="size-3.5 shrink-0 text-muted-foreground" />}
                </button>
              )
            })
          )}
        </div>
      </div>
    </div>
  )
}

/** 实例状态 → status 变量后缀（命中阈值色）。neutral 归 info 以有可见色点。 */
function statusColor(status?: string): string {
  const level = instanceStatusLevel(status ?? '')
  return level === 'neutral' ? 'info' : level
}

/** 把第 idx 行滚入可视区（键盘移动时）。越界/无元素则忽略。 */
function scrollIntoView(container: HTMLElement | null, idx: number): void {
  if (!container || idx < 0) return
  const row = container.querySelector<HTMLElement>(`[data-palette-row="${idx}"]`)
  row?.scrollIntoView({ block: 'nearest' })
}
