import { useCallback, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  ExternalLink,
  PanelTop,
  Plus,
  X,
  AppWindow,
} from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { cn } from '@jianmanager/ui'
import ResourceExplorer from './ResourceExplorer'
import {
  emptyTabsState,
  openTab,
  closeTab,
  activateTab,
  floatTab,
  dockTab,
  updateTabContext,
  type ExplorerTabsState,
} from './explorer-tabs'

interface ExplorerTabHostProps {
  instanceId: number
  /** 可选初始目录（深链页可传）。 */
  initialDir?: string
  initialFile?: string
}

interface FloatGeom {
  x: number
  y: number
  w: number
  h: number
}

const MIN_W = 480
const MIN_H = 360
const DEFAULT_W = 720
const DEFAULT_H = 520

function defaultGeom(index: number): FloatGeom {
  return {
    x: 48 + index * 28,
    y: 72 + index * 28,
    w: DEFAULT_W,
    h: DEFAULT_H,
  }
}

/**
 * 资源管理器多标签 + 浮动宿主（FR-376）。
 * 每个标签独立 ResourceExplorer（key=tabId）；浮动时同一实例改 fixed 定位，不 remount 以保状态。
 */
export default function ExplorerTabHost({
  instanceId,
  initialDir = '',
  initialFile,
}: ExplorerTabHostProps) {
  const { t } = useTranslation()
  const [state, setState] = useState<ExplorerTabsState>(() =>
    emptyTabsState({ currentDir: initialDir, openFilePath: initialFile }),
  )
  const [geoms, setGeoms] = useState<Record<string, FloatGeom>>({})
  const [zTop, setZTop] = useState(40)
  const [zMap, setZMap] = useState<Record<string, number>>({})
  const zRef = useRef(40)

  const focusFloat = useCallback((id: string) => {
    zRef.current += 1
    setZTop(zRef.current)
    setZMap((m) => ({ ...m, [id]: zRef.current }))
  }, [])

  const ensureGeom = useCallback((id: string, index: number) => {
    setGeoms((g) => (g[id] ? g : { ...g, [id]: defaultGeom(index) }))
  }, [])

  const onNewTab = () => {
    setState((s) => {
      const r = openTab(s, { currentDir: '' })
      if (!r.ok) {
        toast.error(t('files.tabsMaxReached', { max: 8 }))
        return s
      }
      return r.state
    })
  }

  const onCloseTab = (id: string) => {
    const tab = state.tabs.find((x) => x.id === id)
    if (tab?.dirty) {
      if (!window.confirm(t('files.unsavedConfirm'))) return
    }
    setState((s) => closeTab(s, id))
  }

  const onFloat = (id: string) => {
    setState((s) => {
      const r = floatTab(s, id)
      if (!r.ok) {
        if (r.reason === 'max_floats') toast.error(t('files.floatMaxReached', { max: 3 }))
        return s
      }
      const idx = r.state.tabs.findIndex((x) => x.id === id)
      ensureGeom(id, Math.max(0, idx))
      focusFloat(id)
      return r.state
    })
  }

  const onDock = (id: string) => {
    setState((s) => dockTab(s, id))
  }

  const onOpenBrowser = (id: string) => {
    const tab = state.tabs.find((x) => x.id === id)
    if (!tab) return
    const q = new URLSearchParams()
    if (tab.currentDir) q.set('path', tab.currentDir)
    if (tab.openFilePath) q.set('file', tab.openFilePath)
    const url = `${window.location.origin}/instances/${instanceId}/files${q.toString() ? `?${q}` : ''}`
    window.open(url, '_blank', 'noopener,noreferrer')
  }

  const onContext = useCallback((tabId: string, ctx: { dir: string; file?: string; dirty: boolean }) => {
    setState((s) => {
      const tab = s.tabs.find((x) => x.id === tabId)
      if (!tab) return s
      const nextFile = ctx.file
      if (
        tab.currentDir === ctx.dir &&
        tab.openFilePath === nextFile &&
        tab.dirty === ctx.dirty
      ) {
        return s
      }
      return updateTabContext(s, tabId, {
        dir: ctx.dir,
        file: nextFile ?? null,
        dirty: ctx.dirty,
      })
    })
  }, [])

  // 指针拖拽 / 缩放
  const dragRef = useRef<{
    id: string
    mode: 'move' | 'resize'
    startX: number
    startY: number
    orig: FloatGeom
  } | null>(null)

  const onPointerMove = useCallback((e: PointerEvent) => {
    const d = dragRef.current
    if (!d) return
    const dx = e.clientX - d.startX
    const dy = e.clientY - d.startY
    setGeoms((g) => {
      const cur = g[d.id] ?? d.orig
      if (d.mode === 'move') {
        return {
          ...g,
          [d.id]: {
            ...cur,
            x: Math.max(0, d.orig.x + dx),
            y: Math.max(0, d.orig.y + dy),
          },
        }
      }
      return {
        ...g,
        [d.id]: {
          ...cur,
          w: Math.max(MIN_W, d.orig.w + dx),
          h: Math.max(MIN_H, d.orig.h + dy),
        },
      }
    })
  }, [])

  const onPointerUp = useCallback(() => {
    dragRef.current = null
    window.removeEventListener('pointermove', onPointerMove)
    window.removeEventListener('pointerup', onPointerUp)
  }, [onPointerMove])

  const startDrag = (id: string, mode: 'move' | 'resize', e: ReactPointerEvent) => {
    e.preventDefault()
    focusFloat(id)
    const orig = geoms[id] ?? defaultGeom(0)
    dragRef.current = {
      id,
      mode,
      startX: e.clientX,
      startY: e.clientY,
      orig,
    }
    window.addEventListener('pointermove', onPointerMove)
    window.addEventListener('pointerup', onPointerUp)
  }

  const dockedTabs = state.tabs.filter((t) => !t.floated)
  const activeDocked = dockedTabs.some((t) => t.id === state.activeId)
    ? state.activeId
    : dockedTabs[0]?.id

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="explorer-tab-host">
      {/* 标签条：仅非浮动签 */}
      <div className="flex shrink-0 items-center gap-1 border-b bg-muted/20 px-1 py-1">
        <div className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto">
          {dockedTabs.map((tab) => (
            <div
              key={tab.id}
              className={cn(
                'group flex max-w-[10rem] shrink-0 items-center gap-0.5 rounded-md border px-2 py-1 text-xs',
                tab.id === activeDocked
                  ? 'border-primary/40 bg-background text-foreground'
                  : 'border-transparent bg-transparent text-muted-foreground hover:bg-accent/50',
              )}
            >
              <button
                type="button"
                className="min-w-0 truncate font-medium"
                onClick={() => setState((s) => activateTab(s, tab.id))}
                title={tab.title}
              >
                {tab.dirty ? '• ' : ''}
                {tab.title}
              </button>
              <button
                type="button"
                className="rounded p-0.5 opacity-60 hover:bg-accent hover:opacity-100"
                title={t('files.floatPop')}
                aria-label={t('files.floatPop')}
                onClick={() => onFloat(tab.id)}
              >
                <PanelTop className="size-3" />
              </button>
              <button
                type="button"
                className="rounded p-0.5 opacity-60 hover:bg-accent hover:opacity-100"
                title={t('files.openInBrowserTab')}
                aria-label={t('files.openInBrowserTab')}
                onClick={() => onOpenBrowser(tab.id)}
              >
                <ExternalLink className="size-3" />
              </button>
              {state.tabs.length > 1 && (
                <button
                  type="button"
                  className="rounded p-0.5 opacity-60 hover:bg-destructive/10 hover:text-destructive hover:opacity-100"
                  title={t('common.close')}
                  aria-label={t('common.close')}
                  onClick={() => onCloseTab(tab.id)}
                >
                  <X className="size-3" />
                </button>
              )}
            </div>
          ))}
        </div>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 shrink-0 gap-1 px-2 text-xs"
          onClick={onNewTab}
          title={t('files.newTab')}
        >
          <Plus className="size-3.5" />
          {t('files.newTab')}
        </Button>
      </div>

      {/* 停靠内容区 + 各标签 Explorer（浮动的用 fixed） */}
      <div className="relative min-h-0 flex-1">
        {state.tabs.map((tab, index) => {
          const floated = tab.floated
          const geom = geoms[tab.id] ?? defaultGeom(index)
          const visibleDocked = !floated && tab.id === activeDocked
          return (
            <div
              key={tab.id}
              data-testid={floated ? `explorer-float-${tab.id}` : `explorer-pane-${tab.id}`}
              data-floated={floated ? '1' : '0'}
              className={cn(
                floated
                  ? 'fixed flex flex-col overflow-hidden rounded-lg border bg-background shadow-xl'
                  : visibleDocked
                    ? 'absolute inset-0 flex flex-col'
                    : 'hidden',
              )}
              style={
                floated
                  ? {
                      left: geom.x,
                      top: geom.y,
                      width: geom.w,
                      height: geom.h,
                      zIndex: zMap[tab.id] ?? zTop,
                    }
                  : undefined
              }
              onMouseDown={() => {
                if (floated) focusFloat(tab.id)
              }}
            >
              {floated && (
                <div
                  className="flex shrink-0 cursor-move items-center gap-1 border-b bg-muted/40 px-2 py-1 text-xs"
                  onPointerDown={(e) => startDrag(tab.id, 'move', e)}
                >
                  <AppWindow className="size-3.5 text-muted-foreground" />
                  <span className="min-w-0 flex-1 truncate font-medium">
                    {tab.dirty ? '• ' : ''}
                    {tab.title}
                  </span>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className="h-6 px-1.5 text-xs"
                    onClick={() => onDock(tab.id)}
                    onPointerDown={(e) => e.stopPropagation()}
                  >
                    {t('files.floatDock')}
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className="h-6 px-1.5"
                    title={t('files.openInBrowserTab')}
                    onClick={() => onOpenBrowser(tab.id)}
                    onPointerDown={(e) => e.stopPropagation()}
                  >
                    <ExternalLink className="size-3" />
                  </Button>
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    className="h-6 px-1.5"
                    title={t('common.close')}
                    onClick={() => onCloseTab(tab.id)}
                    onPointerDown={(e) => e.stopPropagation()}
                  >
                    <X className="size-3" />
                  </Button>
                </div>
              )}
              <div className="min-h-0 flex-1">
                <ResourceExplorer
                  instanceId={instanceId}
                  initialDir={tab.currentDir}
                  initialFile={tab.openFilePath}
                  draftKey={`resource-tab:${tab.id}`}
                  onContextChange={(ctx) => onContext(tab.id, ctx)}
                />
              </div>
              {floated && (
                <div
                  className="absolute bottom-0 right-0 h-3 w-3 cursor-se-resize"
                  onPointerDown={(e) => startDrag(tab.id, 'resize', e)}
                  aria-hidden
                />
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
