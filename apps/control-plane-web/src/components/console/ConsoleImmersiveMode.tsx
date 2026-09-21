import { useCallback, useEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { Maximize2, Minimize2, PanelLeftClose, PanelTop, Plus, RefreshCw, X, type LucideIcon } from 'lucide-react'
import { toast } from 'sonner'

import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { useInstance, useInstanceSearch } from '@/api/instances'
import { useInstanceMetrics } from '@/api/metrics'
import {
  clampRatio,
  findPane,
  instanceIdsInTrees,
  paneCount,
  paneLeaves,
  removePane,
  replacePaneInstance,
  reidTree,
  setSplitRatio,
  splitPane,
  type ImmersivePaneLeaf,
  type ImmersivePaneSplit,
  type ImmersivePaneTree,
  type PaneSplitDirection,
} from '@/lib/console-immersive-layout'
import { terminalSessionManager } from '@/lib/terminal-session-manager'

type PickerRequest =
  | { kind: 'split'; paneId: string; direction: PaneSplitDirection }
  | { kind: 'replace'; paneId: string }

export interface ImmersivePaneRenderProps {
  instanceId: number
  paneId: string
  focused: boolean
  onFocus: () => void
}

export interface ConsoleImmersiveModeProps {
  initialInstanceId: number
  onExit: () => void
  renderPane: (props: ImmersivePaneRenderProps) => ReactNode
}

const MAX_PANES_DESKTOP = 4

/** 布局快照的 sessionStorage 键：进出沉浸/导航往返后工作台原样恢复。 */
const LAYOUT_STORAGE_KEY = 'console.immersive.layout'

/** 裸 ESC 双击退出的判定窗口（毫秒）。 */
const ESC_EXIT_WINDOW_MS = 1600

/**
 * 按视口宽度分级 pane 上限（可用性增强）：
 * <768px 单 pane（再加只会得到读不了的 90px 小格）、<1280px 至多 2、宽屏 4。
 */
function readMaxPanes(): number {
  if (typeof window === 'undefined') return MAX_PANES_DESKTOP
  const width = window.innerWidth
  if (width >= 1280) return MAX_PANES_DESKTOP
  if (width >= 768) return 2
  return 1
}

interface StoredImmersiveLayout {
  tree: ImmersivePaneTree
  focusedPaneId: string
}

/** 读布局快照；做最小形状校验，脏数据/旧格式一律当不存在（宁重开不崩）。 */
function readStoredLayout(): StoredImmersiveLayout | null {
  try {
    const raw = sessionStorage.getItem(LAYOUT_STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as StoredImmersiveLayout | null
    if (!parsed || parsed.tree?.type == null || typeof parsed.focusedPaneId !== 'string') return null
    for (const leaf of paneLeaves(parsed.tree)) {
      if (typeof leaf.instanceId !== 'number') return null
    }
    return parsed
  } catch {
    return null
  }
}

/** 按窗口长宽比挑默认分割方向：竖屏上下分（横切两半各自太窄没法看）。 */
function pickSplitDirection(): PaneSplitDirection {
  if (typeof window !== 'undefined' && window.innerWidth < window.innerHeight) return 'vertical'
  return 'horizontal'
}

/**
 * 沉浸控制台工作台（ADR-087）。
 *
 * 它不是 tmux 仿真：F11/Esc 只负责进出，全局不抢 Ctrl+B 或复制键。多实例和分屏通过
 * 始终可见的顶部布局按钮与 pane 标题栏完成，日志/输入保持在原有 DOM 输出 + 原生输入架构上。
 */
export default function ConsoleImmersiveMode({ initialInstanceId, onExit, renderPane }: ConsoleImmersiveModeProps) {
  const { t } = useTranslation()
  const ids = useRef(0)
  // 恢复上次工作台布局（可用性增强）：快照 leaf 全部重编 id，避免与新会话的
  // `pane-<instanceId>-<n>` 生成序列撞车（React key 冲突 + 焦点错乱）。
  // 实例已删除的 pane 照常渲染：标题栏落 #id，「更换实例」一键可救。
  // useState 初始化器只跑一次，且用局部计数器而非 ref（渲染期不碰 ref）。
  const [restored] = useState(() => {
    const snapshot = readStoredLayout()
    if (!snapshot) return null
    let seq = 0
    const idMap = new Map<string, string>()
    const tree = reidTree(snapshot.tree, (oldId) => {
      const next = `pane-restored-${seq++}`
      idMap.set(oldId, next)
      return next
    })
    const focusedPaneId = idMap.get(snapshot.focusedPaneId) ?? paneLeaves(tree)[0]!.id
    return { tree, focusedPaneId }
  })
  const initialPane = useMemo<ImmersivePaneLeaf>(
    () => ({ type: 'leaf', id: `pane-${initialInstanceId}-0`, instanceId: initialInstanceId }),
    [initialInstanceId],
  )
  const [layout, setLayout] = useState<ImmersivePaneTree>(() => restored?.tree ?? initialPane)
  const [focusedPaneId, setFocusedPaneId] = useState<string>(restored?.focusedPaneId ?? initialPane.id)
  const [maximizedPaneId, setMaximizedPaneId] = useState<string | null>(null)
  const [picker, setPicker] = useState<PickerRequest | null>(null)
  const [pickerQuery, setPickerQuery] = useState('')
  const [pickerIndex, setPickerIndex] = useState(0)
  // 视口分级 pane 上限：窗口缩放实时跟随（拖出分屏后再缩窗也不会积压成小格子）。
  const [maxPanes, setMaxPanes] = useState(readMaxPanes)
  useEffect(() => {
    const sync = () => setMaxPanes(readMaxPanes())
    window.addEventListener('resize', sync)
    return () => window.removeEventListener('resize', sync)
  }, [])

  // 布局持久化：写入幂等且量级极小（几 KB），随布局/焦点变化直接落。
  useEffect(() => {
    try {
      sessionStorage.setItem(LAYOUT_STORAGE_KEY, JSON.stringify({ tree: layout, focusedPaneId }))
    } catch { /* 隐私模式等存不进就算了 */ }
  }, [layout, focusedPaneId])

  const focusedPane = findPane(layout, focusedPaneId) ?? paneLeaves(layout)[0]
  const sessionIds = useMemo(() => instanceIdsInTrees([layout]), [layout])
  const visiblePaneIds = useMemo(
    () => (maximizedPaneId ? [maximizedPaneId] : paneLeaves(layout).map((pane) => pane.id)),
    [layout, maximizedPaneId],
  )
  const { data: instanceSearch, isFetching: isSearchingInstances } = useInstanceSearch(
    { q: pickerQuery || undefined, page: 1, pageSize: 20 },
    picker !== null,
  )

  const makePane = useCallback((instanceId: number): ImmersivePaneLeaf => {
    ids.current += 1
    return { type: 'leaf', id: `pane-${instanceId}-${ids.current}`, instanceId }
  }, [])

  const focusPane = useCallback((paneId: string) => setFocusedPaneId(paneId), [])

  const requestSplit = useCallback(
    (direction: PaneSplitDirection) => {
      if (paneCount(layout) >= maxPanes) {
        toast.error(t('instanceDetail.consoleImmersivePaneLimit', { count: maxPanes }))
        return
      }
      setPicker({ kind: 'split', paneId: focusedPane.id, direction })
      setPickerQuery('')
      setPickerIndex(0)
    },
    [focusedPane.id, layout, maxPanes, t],
  )

  /** 拖拽分隔条：按引用定位树节点更新占比（见 setSplitRatio）。 */
  const changeRatio = useCallback((target: ImmersivePaneSplit, ratio: number) => {
    setLayout((current) => setSplitRatio(current, target, ratio))
  }, [])

  const chooseInstance = useCallback(
    (instanceId: number) => {
      if (!picker) return
      if (picker.kind === 'replace') {
        setLayout((current) => replacePaneInstance(current, picker.paneId, instanceId))
        setFocusedPaneId(picker.paneId)
      } else {
        const pane = makePane(instanceId)
        setLayout((current) => splitPane(current, picker.paneId, picker.direction, pane))
        setFocusedPaneId(pane.id)
      }
      setMaximizedPaneId(null)
      setPicker(null)
      setPickerQuery('')
    },
    [makePane, picker],
  )

  const closePane = useCallback(
    (paneId: string) => {
      const next = removePane(layout, paneId)
      if (!next) {
        toast.error(t('instanceDetail.consoleImmersiveLastPane'))
        return
      }
      const nextFocus = paneLeaves(next)[0]
      setLayout(next)
      setFocusedPaneId(nextFocus.id)
      setMaximizedPaneId(null)
    },
    [layout, t],
  )

  const toggleMaximize = useCallback((paneId: string) => {
    setMaximizedPaneId((current) => (current === paneId ? null : paneId))
    setFocusedPaneId(paneId)
  }, [])

  // 维持 ADR-067 会话保活：可见 pane 标前台，其他 pane 标后台；本层自行创建的会话在移除
  // pane 后释放，进入前已存在的实例会话仍交由外层热缓存负责。
  const managedSessionsRef = useRef(new Map<number, boolean>())
  const sessionIdsKey = sessionIds.join(',')
  useEffect(() => {
    const wanted = new Set(sessionIds)
    for (const id of sessionIds) {
      if (managedSessionsRef.current.has(id)) continue
      managedSessionsRef.current.set(id, terminalSessionManager.hasSession(id))
      terminalSessionManager.pin(id)
    }
    for (const [id, existedBefore] of managedSessionsRef.current) {
      if (wanted.has(id)) continue
      terminalSessionManager.markHidden(id)
      terminalSessionManager.unpin(id)
      if (!existedBefore) terminalSessionManager.dispose(id)
      managedSessionsRef.current.delete(id)
    }
  }, [sessionIds, sessionIdsKey])

  useEffect(
    () => () => {
      for (const [id, existedBefore] of managedSessionsRef.current) {
        terminalSessionManager.markHidden(id)
        terminalSessionManager.unpin(id)
        if (!existedBefore) terminalSessionManager.dispose(id)
      }
      managedSessionsRef.current.clear()
    },
    [],
  )

  useEffect(() => {
    const visibleInstances = new Set(
      paneLeaves(layout)
        .filter((pane) => visiblePaneIds.includes(pane.id))
        .map((pane) => pane.instanceId),
    )
    for (const id of sessionIds) {
      if (visibleInstances.has(id)) terminalSessionManager.markVisible(id)
      else terminalSessionManager.markHidden(id)
    }
  }, [layout, sessionIds, sessionIdsKey, visiblePaneIds])

  useEffect(() => {
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => {
      document.body.style.overflow = previousOverflow
    }
  }, [])

  // 有且仅有进出键：不注册 Ctrl+B、方向键或复制快捷键，命令栏始终保留浏览器原生语义。
  //
  // ESC 语义分层（修「按 ESC 关补全却退出整个工作台」）：捕获层只在这些情况接管——
  // ① 拾取器打开：关拾取器；② 焦点在 input/textarea/contenteditable 外的**裸 ESC**：
  // 第一次只弹提示（内层的清选区/关菜单等语义照常先跑），窗口期内再按一次才退出。
  // 输入类元素里的 ESC 完全放行，归命令栏补全 / Ctrl+R 反向搜索 / 日志搜索框自己消费。
  const lastEscapeAtRef = useRef(0)
  useEffect(() => {
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'F11') {
        event.preventDefault()
        onExit()
        return
      }
      if (event.key !== 'Escape') return
      if (picker) {
        event.preventDefault()
        setPicker(null)
        setPickerQuery('')
        setPickerIndex(0)
        return
      }
      const target = event.target as HTMLElement | null
      // window/document 上没有 closest（jsdom 里 fireEvent(window) 也走这里），统一按"非输入"处理。
      if (target && typeof target.closest === 'function' && target.closest('input, textarea, [contenteditable="true"]')) return
      const now = Date.now()
      if (now - lastEscapeAtRef.current <= ESC_EXIT_WINDOW_MS) {
        event.preventDefault()
        onExit()
        return
      }
      lastEscapeAtRef.current = now
      toast(t('instanceDetail.consoleImmersiveEscHint'))
    }
    window.addEventListener('keydown', handleKeyDown, true)
    return () => window.removeEventListener('keydown', handleKeyDown, true)
  }, [onExit, picker, t])

  // basis：被父 split 指定份额（拖拽后的 ratio）；未指定走 flex-1 均分。
  const renderTree = (tree: ImmersivePaneTree, basis?: string): ReactNode => {
    if (tree.type === 'leaf') {
      const focused = tree.id === focusedPane.id
      return (
        <ImmersivePane
          key={tree.id}
          pane={tree}
          focused={focused}
          maximized={maximizedPaneId === tree.id}
          canClose={paneCount(layout) > 1}
          basis={basis}
          onFocus={() => focusPane(tree.id)}
          onReplace={() => {
            setPicker({ kind: 'replace', paneId: tree.id })
            setPickerQuery('')
            setPickerIndex(0)
          }}
          onToggleMaximize={() => toggleMaximize(tree.id)}
          onClose={() => closePane(tree.id)}
        >
          {renderPane({ instanceId: tree.instanceId, paneId: tree.id, focused, onFocus: () => focusPane(tree.id) })}
        </ImmersivePane>
      )
    }
    return (
      <div
        key={`${tree.direction}-${paneLeaves(tree).map((pane) => pane.id).join('-')}`}
        style={basis ? { flex: `0 0 ${basis}` } : undefined}
        className={cn('flex min-h-0 min-w-0 flex-1', tree.direction === 'horizontal' ? 'flex-row' : 'flex-col')}
      >
        {renderTree(tree.first, tree.ratio != null ? `${tree.ratio * 100}%` : undefined)}
        <PaneResizer
          direction={tree.direction}
          ratio={tree.ratio ?? 0.5}
          onResize={(next) => changeRatio(tree, next)}
        />
        {renderTree(tree.second)}
      </div>
    )
  }

  const pickerItems = instanceSearch?.items ?? []
  const activePickerIndex = Math.min(pickerIndex, Math.max(0, pickerItems.length - 1))
  // 键盘高亮跟随滚动：↑↓ 选中项保持在可视区（WAI-ARIA combobox 惯例）。
  // scrollIntoView 带存在性守卫：jsdom 未实现该 API，测试环境不应炸。
  useEffect(() => {
    if (!picker) return
    const active = document.querySelector<HTMLElement>('[data-picker-option="true"][data-picker-active="true"]')
    active?.scrollIntoView?.({ block: 'nearest' })
  }, [activePickerIndex, picker])

  if (typeof document === 'undefined') return null

  return createPortal(
    <div
      data-testid="console-immersive-mode"
      aria-label={t('instanceDetail.consoleImmersiveLabel')}
      className="fixed inset-0 z-[100] flex min-h-0 flex-col overflow-hidden bg-[#0f1115] text-slate-100"
    >
      <ImmersiveHeader
        instanceId={focusedPane.instanceId}
        paneCount={paneCount(layout)}
        maxPanes={maxPanes}
        maximized={maximizedPaneId !== null}
        onAdd={() => requestSplit(pickSplitDirection())}
        onSplitHorizontal={() => requestSplit('horizontal')}
        onSplitVertical={() => requestSplit('vertical')}
        onExit={onExit}
      />

      <main className="relative flex min-h-0 flex-1 overflow-hidden bg-[radial-gradient(circle_at_top,#20252e_0%,#151920_44%,#0f1115_100%)] p-3 sm:p-5">
        <div className="flex min-h-0 min-w-0 flex-1">
          {maximizedPaneId ? renderTree(findPane(layout, maximizedPaneId) ?? focusedPane) : renderTree(layout)}
        </div>
      </main>

      <Dialog
        open={picker !== null}
        onOpenChange={(open) => {
          if (!open) {
            setPicker(null)
            setPickerQuery('')
          }
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>{t('instanceDetail.consoleImmersivePickerTitle')}</DialogTitle>
            <DialogDescription>
              {picker?.kind === 'replace'
                ? t('instanceDetail.consoleImmersivePickerReplaceHint')
                : t('instanceDetail.consoleImmersivePickerSplitHint')}
            </DialogDescription>
          </DialogHeader>
          <input
            value={pickerQuery}
            onChange={(event) => {
              setPickerQuery(event.target.value)
              setPickerIndex(0)
            }}
            onKeyDown={(event) => {
              // 拾取器键盘化：↑↓ 移高亮、Enter 选中，全程不必碰鼠标。
              if (event.key === 'ArrowDown') {
                event.preventDefault()
                setPickerIndex((index) => Math.min(index + 1, pickerItems.length - 1))
              } else if (event.key === 'ArrowUp') {
                event.preventDefault()
                setPickerIndex((index) => Math.max(0, index - 1))
              } else if (event.key === 'Enter') {
                const picked = pickerItems[activePickerIndex]
                if (picked) {
                  event.preventDefault()
                  chooseInstance(picked.id)
                }
              }
            }}
            placeholder={t('instanceDetail.consoleImmersiveInstanceSearch')}
            aria-label={t('instanceDetail.consoleImmersiveInstanceSearch')}
            autoFocus
            className="h-10 w-full rounded-lg border bg-background px-3 text-sm outline-none focus:border-primary"
          />
          <div className="max-h-80 space-y-1 overflow-y-auto rounded-lg border bg-muted/20 p-1">
            {isSearchingInstances ? (
              <p className="px-3 py-4 text-sm text-muted-foreground">{t('common.loading')}</p>
            ) : pickerItems.length === 0 ? (
              <p className="px-3 py-4 text-sm text-muted-foreground">{t('instanceDetail.consoleImmersiveNoInstance')}</p>
            ) : (
              pickerItems.map((instance, index) => (
                <button
                  key={instance.id}
                  type="button"
                  data-picker-option="true"
                  data-picker-active={index === activePickerIndex ? 'true' : undefined}
                  onClick={() => chooseInstance(instance.id)}
                  className={cn(
                    'flex w-full items-center justify-between rounded-md px-3 py-2.5 text-left transition-colors hover:bg-accent',
                    index === activePickerIndex && 'bg-accent',
                  )}
                >
                  <span className="min-w-0 truncate font-medium">{instance.name}</span>
                  <span className="ml-3 shrink-0 font-mono text-xs text-muted-foreground">{instance.status}</span>
                </button>
              ))
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => setPicker(null)}>{t('common.cancel')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>,
    document.body,
  )
}

function ImmersiveHeader({
  instanceId,
  paneCount: count,
  maxPanes,
  maximized,
  onAdd,
  onSplitHorizontal,
  onSplitVertical,
  onExit,
}: {
  instanceId: number
  paneCount: number
  maxPanes: number
  maximized: boolean
  onAdd: () => void
  onSplitHorizontal: () => void
  onSplitVertical: () => void
  onExit: () => void
}) {
  const { t } = useTranslation()
  const { data: instance } = useInstance(instanceId)
  const { data: metrics } = useInstanceMetrics(instanceId)
  const probeAvailable = metrics?.probeAvailable ?? false

  return (
    // 方案 A 命令枢纽条：单行 h-11——身份（状态点+实例名+状态徽章）| 指标段 | 动作组。
    // 去掉 JM logo、两行标题块与「沉浸工作台」装饰小字：沉浸态的屏幕是留给内容的。
    <header className="flex h-11 shrink-0 items-center gap-3 border-b border-white/10 bg-[#171a21]/95 px-3 shadow-2xl backdrop-blur-xl sm:px-4">
      <span className="flex min-w-0 items-center gap-2">
        <span
          aria-hidden
          className={cn('size-2 shrink-0 rounded-full', instance?.status === 'RUNNING' ? 'bg-emerald-400' : 'bg-slate-500')}
        />
        <b className="truncate font-mono text-[13px] text-slate-100">{instance?.name ?? `#${instanceId}`}</b>
        <span
          className={cn(
            'hidden shrink-0 rounded px-1.5 py-px text-[10px] font-medium sm:inline',
            instance?.status === 'RUNNING' ? 'bg-cyan-400/10 text-cyan-300' : 'bg-slate-500/15 text-slate-300',
          )}
        >
          {instance?.status ?? t('common.loading')}
        </span>
      </span>

      <span aria-hidden className="h-3.5 w-px shrink-0 bg-white/10" />

      <span className="flex min-w-0 items-center gap-3 overflow-x-auto">
        {/* TPS 仅探针可得；在线数由探针 → SLP → Query 任一来源提供。缺测显「不可用」，
            不以 0 冒充「0 人在线」（FR-446/447）。 */}
        {probeAvailable ? (
          <MetricSegment label="TPS" value={metrics?.tps.toFixed(1) ?? '—'} />
        ) : (
          <MetricSegment label="TPS" value={t('metrics.unavailable')} />
        )}
        <MetricSegment label={t('serverConsole.online')} value={metrics?.playersAvailable ? String(metrics.onlinePlayers) : t('metrics.unavailable')} />
        <MetricSegment label="CPU" value={metrics ? `${Math.round(metrics.cpuPercent)}%` : '—'} />
        <MetricSegment label={t('instanceDetail.consoleImmersivePaneCount')} value={`${count}/${maxPanes}`} />
      </span>

      <span className="ml-auto flex shrink-0 items-center gap-1.5">
        <HeaderAction icon={Plus} label={t('instanceDetail.consoleImmersiveAddPane')} onClick={onAdd} variant="primary" />
        <HeaderAction icon={PanelLeftClose} label={t('instanceDetail.consoleImmersiveSplitHorizontal')} onClick={onSplitHorizontal} disabled={maximized} iconOnly />
        <HeaderAction icon={PanelTop} label={t('instanceDetail.consoleImmersiveSplitVertical')} onClick={onSplitVertical} disabled={maximized} iconOnly />
        <button
          type="button"
          aria-label={t('instanceDetail.consoleImmersiveExit')}
          onClick={onExit}
          className="ml-1 inline-flex h-8 items-center gap-1.5 rounded-lg border border-white/10 bg-white/5 px-2.5 text-xs font-medium text-slate-200 transition-colors hover:border-red-400/40 hover:bg-red-500/10 hover:text-red-200"
        >
          <X className="size-3.5" />
          <span className="hidden sm:inline">{t('instanceDetail.consoleImmersiveExit')}</span>
        </button>
      </span>
    </header>
  )
}

function HeaderAction({
  icon: Icon,
  label,
  onClick,
  disabled = false,
  variant = 'ghost',
  iconOnly = false,
}: {
  icon: LucideIcon
  label: string
  onClick: () => void
  disabled?: boolean
  /** 视觉层级（可用性走查）：primary=主动作（添加控制台），ghost=中性，danger=破坏/退出向。 */
  variant?: 'primary' | 'ghost' | 'danger'
  /** iconOnly：只渲染图标（aria-label/title 兜底语义），用于分屏类高频紧凑动作。 */
  iconOnly?: boolean
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        'inline-flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-slate-300 transition-colors disabled:cursor-not-allowed disabled:opacity-35',
        variant === 'primary' && 'border border-transparent bg-slate-100 font-medium text-slate-900 hover:bg-white hover:text-slate-900',
        variant === 'ghost' && 'border border-white/10 bg-white/5 hover:bg-white/10 hover:text-white',
        variant === 'danger' && 'border border-white/10 bg-white/5 hover:border-red-400/40 hover:bg-red-500/10 hover:text-red-200',
      )}
    >
      <Icon className="size-4" />
      {!iconOnly && <span className="hidden text-xs font-medium sm:inline">{label}</span>}
    </button>
  )
}

/** 顶栏指标段（方案 A 命令枢纽条）：无边框 label+mono 值，融入单行头部而非浮凸芯片。 */
function MetricSegment({ label, value }: { label: string; value: string }) {
  return (
    <span className="inline-flex shrink-0 items-baseline gap-1.5">
      {/* slate-500 在石墨底上只有 ~3.4:1，10px 字号过不了 WCAG AA，提到 slate-400。 */}
      <span className="whitespace-nowrap text-[11px] text-slate-400">{label}</span>
      <b className="whitespace-nowrap font-mono text-xs font-semibold tabular-nums text-slate-200">{value}</b>
    </span>
  )
}

function ImmersivePane({
  pane,
  focused,
  maximized,
  canClose,
  basis,
  onFocus,
  onReplace,
  onToggleMaximize,
  onClose,
  children,
}: {
  pane: ImmersivePaneLeaf
  focused: boolean
  maximized: boolean
  canClose: boolean
  /** 父 split 指定的份额（拖拽后的 ratio）；未指定走 flex-1 均分。 */
  basis?: string
  onFocus: () => void
  onReplace: () => void
  onToggleMaximize: () => void
  onClose: () => void
  children: ReactNode
}) {
  const { t } = useTranslation()
  const { data: instance } = useInstance(pane.instanceId)
  const status = instance?.status ?? 'LOADING'

  return (
    <section
      data-testid={`console-immersive-pane-${pane.id}`}
      data-console-immersive-focused={focused ? 'true' : undefined}
      style={basis ? { flex: `0 0 ${basis}` } : undefined}
      className={cn(
        'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-xl border bg-[#161a22] shadow-[0_20px_60px_rgba(0,0,0,0.28)] transition-shadow',
        focused ? 'border-primary/70 ring-1 ring-primary/30' : 'border-white/10 hover:border-white/20',
      )}
      // Pointer Events 统一鼠标/触屏/触控笔：原先只听 mousedown，部分触屏浏览器
      // 点按 pane 根本不聚焦，分屏操作无从谈起。
      onPointerDownCapture={onFocus}
      onFocusCapture={onFocus}
    >
      <header className="flex h-10 shrink-0 items-center gap-3 border-b border-white/8 bg-white/[0.025] px-3">
        <span className={cn('size-2 shrink-0 rounded-full', status === 'RUNNING' ? 'bg-emerald-400 shadow-[0_0_12px_rgba(52,211,153,0.75)]' : 'bg-slate-500')} />
        <div className="min-w-0 flex-1">
          <p className="truncate font-mono text-xs font-semibold text-slate-100">{instance?.name ?? `#${pane.instanceId}`}</p>
          <p className="truncate text-[10px] text-slate-500">{status}</p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <PaneAction label={t('instanceDetail.consoleImmersiveChangeInstance')} onClick={onReplace}>
            <RefreshCw className="size-3.5 shrink-0" />
            <span className="hidden px-0.5 text-[11px] font-medium sm:inline">{t('instanceDetail.consoleImmersiveChangeInstance')}</span>
          </PaneAction>
          <PaneAction label={maximized ? t('instanceDetail.consoleImmersiveRestore') : t('instanceDetail.consoleImmersiveMaximize')} onClick={onToggleMaximize}>
            {maximized ? <Minimize2 className="size-3.5" /> : <Maximize2 className="size-3.5" />}
          </PaneAction>
          {canClose && <PaneAction label={t('instanceDetail.consoleImmersiveClosePane')} onClick={onClose}><X className="size-3.5" /></PaneAction>}
        </div>
      </header>
      <div className="min-h-0 min-w-0 flex-1 bg-[#12151c]">{children}</div>
    </section>
  )
}

function PaneAction({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      // inline-flex 而非 grid：图标 + 文字两子元素要横排（grid 会叠成两行）。
      className="inline-flex min-h-7 items-center justify-center rounded-md px-1 text-slate-400 transition-colors hover:bg-white/10 hover:text-white"
    >
      {children}
    </button>
  )
}

/**
 * 分屏拖拽条（可用性增强）：分割占比不再永远 50/50。
 *
 * Pointer Events 一套代码覆盖鼠标/触屏/触控笔；拖拽期间 setPointerCapture，
 * 指针出条体也不丢事件。份额换算以**父 split 容器**的实际尺寸为基准——
 * 嵌套分屏里每层的基准互不相同，不能用窗口尺寸。
 */
function PaneResizer({
  direction,
  ratio,
  onResize,
}: {
  direction: PaneSplitDirection
  ratio: number
  onResize: (next: number) => void
}) {
  const { t } = useTranslation()
  const drag = useRef<{ startPos: number; span: number; startRatio: number } | null>(null)

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    const parent = event.currentTarget.parentElement
    if (!parent) return
    const span = direction === 'horizontal' ? parent.clientWidth : parent.clientHeight
    if (span <= 0) return
    drag.current = {
      startPos: direction === 'horizontal' ? event.clientX : event.clientY,
      span,
      startRatio: ratio,
    }
    event.currentTarget.setPointerCapture?.(event.pointerId)
    event.preventDefault()
  }

  const handlePointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const state = drag.current
    if (!state) return
    const pos = direction === 'horizontal' ? event.clientX : event.clientY
    onResize(clampRatio(state.startRatio + (pos - state.startPos) / state.span))
  }

  const endDrag = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!drag.current) return
    drag.current = null
    event.currentTarget.releasePointerCapture?.(event.pointerId)
  }

  return (
    <div
      data-testid="console-immersive-resizer"
      role="separator"
      aria-orientation={direction === 'horizontal' ? 'vertical' : 'horizontal'}
      aria-label={t('instanceDetail.consoleImmersiveResize')}
      title={t('instanceDetail.consoleImmersiveResize')}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onDoubleClick={() => onResize(0.5)}
      className={cn(
        'group relative z-10 shrink-0 touch-none transition-colors',
        direction === 'horizontal' ? 'w-2 cursor-col-resize hover:bg-white/[0.04]' : 'h-2 cursor-row-resize hover:bg-white/[0.04]',
      )}
    >
      <span
        aria-hidden
        className={cn(
          // 默认就可见（bg-white/15），hover 涨宽并上品牌色——「这里能拖」不靠猜。
          'absolute rounded-full bg-white/15 transition-all group-hover:bg-primary/90',
          direction === 'horizontal'
            ? 'inset-y-2 left-1/2 w-[3px] -translate-x-1/2 group-hover:w-1'
            : 'inset-x-2 top-1/2 h-[3px] -translate-y-1/2 group-hover:h-1',
        )}
      />
    </div>
  )
}
