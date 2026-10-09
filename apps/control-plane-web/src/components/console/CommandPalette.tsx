import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { useQueryClient } from '@tanstack/react-query'
import { useInstanceSearch } from '@/api/instances'
import { useNodes } from '@/api/nodes'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { useConsoleStore } from '@/stores/console'
import { flatNavItems } from '@/lib/shared/nav-config'
import { prefetchRoute } from '@/lib/shared/route-prefetch'
import type { PaletteEntry } from '@/lib/console/command-palette'
import CommandPaletteView from '@/components/views/instances/CommandPalette'

/**
 * 全局命令面板的应用接线层（ADR-097 a 范式）。
 *
 * 面板本体是受控视图（见 components/views）；本层持有开合（含全局 Ctrl/⌘+K 监听）、四类数据源
 * （实例走服务端搜索、节点、页面来自导航配置、操作静态）、以及「执行结果」的跳转与副作用。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function CommandPalette() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const role = useAuthStore((s) => s.role)
  const permNodes = usePermissionsStore((s) => s.nodes)
  const permAdmin = usePermissionsStore((s) => s.isPlatformAdmin)
  const permLoaded = usePermissionsStore((s) => s.loaded)
  const open = useConsoleStore((s) => s.commandPaletteOpen)
  const setOpen = useConsoleStore((s) => s.setCommandPaletteOpen)
  const toggleSidebar = useConsoleStore((s) => s.toggleSidebar)
  const selectedNodeId = useConsoleStore((s) => s.selectedNodeId)
  const setSelectedNodeId = useConsoleStore((s) => s.setSelectedNodeId)

  const [query, setQuery] = useState('')
  const trimmedQuery = query.trim()
  const instanceSearchParams = useMemo(
    () => ({
      q: trimmedQuery || undefined,
      nodeId: selectedNodeId ?? undefined,
      page: 1,
      pageSize: 8,
      sort: 'name' as const,
      order: 'asc' as const,
    }),
    [trimmedQuery, selectedNodeId],
  )

  // 实例结果走 FR-247 服务端分页搜索；节点/页面/操作仍本地轻量匹配。
  const { data: instanceSearch, isFetching: searchingInstances } = useInstanceSearch(instanceSearchParams, open)
  const { data: nodes } = useNodes()

  // 操作类目标（静态）：执行副作用而非跳转。
  const commands = useMemo(
    () => [
      { id: 'refresh', label: t('palette.cmdRefresh') },
      { id: 'toggle-sidebar', label: t('palette.cmdToggleSidebar') },
    ],
    [t],
  )

  const pages = useMemo(() => {
    if (permLoaded) return flatNavItems(permNodes, permAdmin).map((n) => ({ to: n.to, label: t(n.labelKey) }))
    return flatNavItems(role).map((n) => ({ to: n.to, label: t(n.labelKey) }))
  }, [role, permNodes, permAdmin, permLoaded, t])

  // 全局 Ctrl/⌘+K 切换（始终挂载的监听；事件回调里 setState 合法，非 effect body）。
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setQuery('')
        setOpen(!useConsoleStore.getState().commandPaletteOpen)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [setOpen, setQuery])

  return (
    <CommandPaletteView
      open={open}
      onOpenChange={(next) => {
        if (!next) setOpen(false)
      }}
      query={query}
      onQueryChange={setQuery}
      pages={pages}
      commands={commands}
      instances={(instanceSearch?.items ?? []).map((i) => ({
        id: i.id,
        name: i.name,
        uuid: i.uuid,
        status: i.status,
        nodeId: i.nodeId,
      }))}
      nodes={(nodes ?? []).map((n) => ({ id: n.id, name: n.name, host: n.host }))}
      nodeScopeId={selectedNodeId}
      searching={searchingInstances}
      onPrefetchRoute={prefetchRoute}
      onSelect={(entry: PaletteEntry) => {
        const [kind, rest] = [entry.kind, entry.key.slice(entry.kind.length + 1)]
        if (kind === 'instance') {
          navigate(`/instances/${rest}`)
        } else if (kind === 'node') {
          setSelectedNodeId(Number(rest))
          navigate(`/nodes?node=${rest}`)
        } else if (kind === 'page') {
          navigate(rest)
        } else if (kind === 'command') {
          if (rest === 'refresh') void queryClient.invalidateQueries()
          else if (rest === 'toggle-sidebar') toggleSidebar()
        }
      }}
    />
  )
}
