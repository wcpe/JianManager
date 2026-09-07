import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { CornerDownLeft, Copy, History, WrapText } from 'lucide-react'

import { cn } from '@jianmanager/ui'

import { useOnlinePlayers } from '@/api/players'
import { copyToClipboard } from '@/lib/clipboard'
import { loadCommandHistory, pushCommandHistory } from '@/lib/console-command-history'
import {
  consoleErrorLines,
  consoleLinesToText,
  filterConsoleLines,
  type ConsoleLevelFilter,
} from '@/lib/console-filter'
import { findStartupSeq, mergeHistoryAndLive, useConsoleHistory, type HistoryJumpOutcome } from '@/lib/console-history'
import { applyPlayerChunk } from '@/lib/console-players'
import { findConsoleMatches, type ConsoleSearchMatch } from '@/lib/console-search'
import { useDirectorRender } from '@/lib/director-render'
import { terminalSessionManager, type FetchTerminalCreds } from '@/lib/terminal-session-manager'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@jianmanager/ui/components/dialog'
import { Button } from '@jianmanager/ui/components/button'
import ConsoleCommandBar from './ConsoleCommandBar'
import ConsoleOutputView, { type ConsoleOutputHandle } from './ConsoleOutputView'

/**
 * 实例控制台（FR-415，ADR-086）：输出区（只读 DOM 虚拟列表）+ 命令栏（原生 input）。
 *
 * 取代原 `components/Terminal.tsx` 的 xterm 渲染壳。输入路径整体废弃——
 * 不再监听 `onData`、不再手工回显/退格/↑↓ 历史（那套在 stdin 管道上假装 pty 的仿造品）。
 *
 * 会话保活语义不变（ADR-067）：WS 与行缓冲常驻 {@link terminalSessionManager}，
 * 本组件 mount 时 acquire + 订阅，卸载（含 `<Activity>` 隐藏）只退订，不断连、不丢缓冲。
 *
 * 本壳另承载两条依赖 FR-415 行模型的能力：
 * - **FR-416**：命令历史按实例持久化、在线玩家名（两路真实来源取并集）下传命令栏做补全
 * - **FR-417**：级别过滤（先过滤后搜索，使命中计数与屏上高亮一致）、复制范围菜单
 * - **FR-419**：历史回溯——DB 游标分页取更早日志 prepend 到缓冲之前，滚动锚定、
 *   回溯横幅、「跳到本次启动 / 时间点」定位（数据源分层见 console-history.ts）
 */

/**
 * 「跳到时间点 / 本次启动」未能到达时的原因文案（FR-419）。
 *
 * 三种失败必须区分开：已回溯到库里最早、撞上单次回溯页数上限、请求失败。
 * 都塞一句「跳转失败」用户不知道该改时间、该再点一次、还是该看网络。
 */
const JUMP_FAILURE_KEY: Record<Exclude<HistoryJumpOutcome, 'reached'>, string> = {
  exhausted: 'instanceDetail.consoleJumpExhausted',
  capped: 'instanceDetail.consoleJumpCapped',
  failed: 'instanceDetail.consoleJumpFailed',
}

/** 级别过滤档位与其 i18n 标签（FR-417）。 */
const LEVEL_FILTERS: readonly { value: ConsoleLevelFilter; labelKey: string }[] = [
  { value: 'all', labelKey: 'instanceDetail.consoleFilterAll' },
  { value: 'warn', labelKey: 'instanceDetail.consoleFilterWarn' },
  { value: 'error', labelKey: 'instanceDetail.consoleFilterError' },
]

/** 自动换行偏好键（可用性增强）：默认关，等高路径保持原行为。 */
const WRAP_PREF_KEY = 'console.wrapText'

function readWrapPref(): boolean {
  try { return localStorage.getItem(WRAP_PREF_KEY) === '1' } catch { return false }
}

export interface InstanceConsoleViewProps {
  instanceId: number
  /**
   * 拉取一次性终端连接凭据（wsUrl + token）。**每次连接前现取**：
   * 一次性 token 首连即被 CP 消费失效，重连必须重取新 token，否则复用会 401（FR-140）。
   */
  fetchToken?: FetchTerminalCreds
  /** 实例非运行：命令栏禁用，但**保持连接**以看关服/崩溃输出。 */
  readOnly?: boolean
  /** token 正在加载中，显示占位而非尝试连接。 */
  isLoading?: boolean
  fontSize?: number
  /** 外层工具栏控制的搜索框开关。 */
  searchOpen?: boolean
  onSearchOpenChange?: (open: boolean) => void
  /**
   * 会话保活（FR-295，ADR-067）：true=卸载（含 Activity 隐藏）只退订，
   * 连接与缓冲由连接管理器常驻；false（默认）=独立表面语义，卸载即释放会话
   * （未被控制台热集 pin 时）。
   */
  persistSession?: boolean
  /** 命令栏禁用原因（非 RUNNING 时的一行说明，spec §2.2）。 */
  disabledReason?: string
  /** 命令栏的直达动作（停机/崩溃态的「启动实例」按钮）。 */
  disabledAction?: ReactNode
  /** 沉浸工作台的深色表面变体。 */
  immersive?: boolean
}

export default function InstanceConsoleView({
  instanceId,
  fetchToken,
  readOnly = false,
  isLoading = false,
  fontSize = 14,
  searchOpen,
  onSearchOpenChange,
  persistSession = false,
  disabledReason,
  disabledAction,
  immersive = false,
}: InstanceConsoleViewProps) {
  const { t } = useTranslation()

  // 行缓冲经 useSyncExternalStore 消费：快照引用稳定（缓冲内部缓存），
  // 订阅登记挂在管理器而非会话上，故「先订阅、后 acquire」与「会话被 LRU 淘汰」都不漏通知。
  const subscribe = useCallback(
    (onChange: () => void) => terminalSessionManager.subscribeLines(instanceId, onChange),
    [instanceId],
  )
  const getSnapshot = useCallback(() => terminalSessionManager.getLines(instanceId), [instanceId])
  const lines = useSyncExternalStore(subscribe, getSnapshot)
  const droppedCount = terminalSessionManager.getDroppedCount(instanceId)

  // 导播台节流（FR-168 / ADR-035）：非激活场景的 WS 保活但**暂停落行**，
  // 输出由管理器累积，切回激活时一次性 flush。无 Provider 时恒激活（FR-166/167 不变）。
  const { active: directorActive } = useDirectorRender()

  // 命令历史按实例持久化（FR-416）：初值从 localStorage 取，故刷新/重挂载后仍在。
  // 用 useState 惰性初值而非 effect 回填——effect 回填会先渲染一帧空历史，↑ 在那一帧按下即无效。
  const [history, setHistory] = useState<string[]>(() => loadCommandHistory(instanceId))
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [levelFilter, setLevelFilter] = useState<ConsoleLevelFilter>('all')
  // 自动换行偏好（可用性增强）：跨实例与刷新保留，默认关（等高路径零行为变化）。
  const [wrapText, setWrapText] = useState(readWrapPref)
  useEffect(() => {
    try { localStorage.setItem(WRAP_PREF_KEY, wrapText ? '1' : '0') } catch { /* 隐私模式忽略 */ }
  }, [wrapText])
  const outputRef = useRef<ConsoleOutputHandle>(null)
  /**
   * 锚点选区（FR-418）的当前文本。
   *
   * 存 ref 而非 state：右键菜单点下去那一刻读一次就够，选区一动就重渲染整个控制台
   * 纯属浪费——输出区自己已经把高亮画好了。
   */
  const anchorSelectionRef = useRef('')

  // ---- 会话订阅（FR-295，ADR-067）----
  useEffect(() => {
    if (isLoading || !fetchToken) return
    terminalSessionManager.acquire(instanceId, fetchToken)
    return () => {
      terminalSessionManager.detach(instanceId)
      // 独立表面（画布卡片等）卸载即释放；控制台 keep-alive 宿主下只退订。
      if (!persistSession) terminalSessionManager.release(instanceId)
    }
    // 故意不依赖 readOnly/fontSize：实例状态与字号变化不重建订阅、不断连。
  }, [fetchToken, instanceId, isLoading, persistSession])

  useEffect(() => {
    terminalSessionManager.setPaused(instanceId, !directorActive)
  }, [directorActive, instanceId])

  // ---- 在线玩家（FR-416 Tab 补全的候选来源，两路取并集）----
  // ①/players 聚合接口：权威名册，但依赖探针在位（FR-067）。查询走 TanStack 共享缓存，
  //   多处消费只有一次轮询；页签隐藏时（Activity）effects 卸载，轮询自动停。
  const { data: onlineData } = useOnlinePlayers()
  // ②控制台输出里的 join/quit/list 行：零依赖兜底，探针没装时补全仍可用。
  const [parsedPlayers, setParsedPlayers] = useState<readonly string[]>([])
  useEffect(() => {
    if (isLoading || !fetchToken) return
    const names = new Set<string>()
    let pending = ''
    // 订阅原始流而非行快照：按整段增量解析，不用每来一行就重扫 5000 行缓冲。
    return terminalSessionManager.onOutput(instanceId, (text) => {
      const result = applyPlayerChunk(names, pending, text)
      pending = result.pending
      if (result.changed) setParsedPlayers([...names])
    })
    // 与会话订阅同依赖：onOutput 需要会话已存在，故必须排在 acquire 之后（effect 按声明序执行）。
  }, [fetchToken, instanceId, isLoading])

  const players = useMemo(() => {
    const merged = new Set(parsedPlayers)
    for (const player of onlineData?.players ?? []) {
      if (player.instanceId === instanceId) merged.add(player.name)
    }
    return [...merged].sort((a, b) => a.localeCompare(b))
  }, [instanceId, onlineData, parsedPlayers])

  // ---- 命令提交 ----
  const submitCommand = useCallback(
    (line: string) => {
      // 回显与下发都在管理器内完成：回显必须落在常驻缓冲里，才能跨卸载留存。
      terminalSessionManager.sendCommand(instanceId, line)
      // 持久化写盘与内存态同源（相邻去重 + 500 条上限都在模块内，FR-416）。
      setHistory(pushCommandHistory(instanceId, line))
    },
    [instanceId],
  )

  // ---- 历史回溯（FR-419，spec §4）----
  // 接缝取自行缓冲的最早保留时刻：未溢出时等于会话创建时刻；溢出后推进到首行，
  // 让 DB 补齐会话内被挤掉的那段日志（mergeHistoryAndLive 在边界去重）。
  //
  // 级别过滤刻意只在前端应用：FR-417 的档位语义是「≥ 阈值」且**堆栈块整块保留**，而数据库
  // 的 `level` 是精确等值，传 `level=error` 会把同一异常的 stdout 堆栈帧（通常落 INFO）
  // 过滤掉，结果只剩没有调用栈的异常头。历史页先完整取回，再由 filterConsoleLines 保留块语义。
  const historyAnchorAt = terminalSessionManager.getHistoryAnchorAt(instanceId)
  const historyBacktrack = useConsoleHistory({ instanceId, anchorTime: historyAnchorAt })

  /** 历史（DB，seq 为负）在前、实时缓冲在后，拼成一条连续的时间轴。 */
  const allLines = useMemo(() => mergeHistoryAndLive(historyBacktrack.lines, lines), [historyBacktrack.lines, lines])

  // ---- 级别过滤（FR-417）----
  // 过滤在搜索**之前**做：搜索命中计数必须与屏上可见的高亮一一对应，否则「第 3/7 项」
  // 里会有几项落在被滤掉的行上，↑↓ 跳过去看不到任何黄底。
  const visibleLines = useMemo(() => filterConsoleLines(allLines, levelFilter), [allLines, levelFilter])

  // ---- 定位：跳到本次启动 / 跳到时间点（FR-419，spec §4.3）----
  const [jumpTarget, setJumpTarget] = useState<{ seq: number; token: number } | undefined>(undefined)
  const [jumping, setJumping] = useState(false)
  const [timeJumpOpen, setTimeJumpOpen] = useState(false)
  const [timeJumpValue, setTimeJumpValue] = useState('')
  // token 单调递增：连续两次跳到同一行时 seq 不变，靠它让输出区重新定位。
  const jumpTokenRef = useRef(0)

  const locateSeq = useCallback((seq: number) => {
    jumpTokenRef.current += 1
    setJumpTarget({ seq, token: jumpTokenRef.current })
  }, [])

  /** 定位到某个时刻：历史里有就落在第一条不早于它的行，否则落在内存缓冲首行。 */
  const locateTime = useCallback(
    (targetIso: string) => {
      const seq = historyBacktrack.seqAtTime(targetIso)
      if (seq !== null) {
        locateSeq(seq)
        return
      }
      // 目标晚于全部已加载历史 → 落点在内存缓冲那一段，定位到缓冲首行（会话起点）。
      if (lines.length > 0) locateSeq(lines[0].seq)
    },
    [historyBacktrack, lines, locateSeq],
  )

  /**
   * 「跳到本次启动」：先在屏上找（缓冲 + 已加载历史，瞬时无请求），
   * 找不到再去库里问启动行的时间、一路回溯到那儿再定位（spec §4.3）。
   */
  const jumpToStartup = useCallback(async () => {
    const onScreen = findStartupSeq(allLines)
    if (onScreen !== null) {
      locateSeq(onScreen)
      return
    }
    setJumping(true)
    try {
      const time = await historyBacktrack.findStartupTime()
      if (!time) {
        toast.error(t('instanceDetail.consoleJumpStartupMissing'))
        return
      }
      const outcome = await historyBacktrack.loadUntil(time)
      if (outcome !== 'reached') {
        toast.error(t(JUMP_FAILURE_KEY[outcome]))
        return
      }
      locateTime(time)
    } finally {
      setJumping(false)
    }
  }, [allLines, historyBacktrack, locateSeq, locateTime, t])

  /** 「跳到时间…」：一路回溯到覆盖该时刻，再定位到第一条不早于它的行。 */
  const jumpToTime = useCallback(
    async (targetIso: string) => {
      setJumping(true)
      try {
        const outcome = await historyBacktrack.loadUntil(targetIso)
        if (outcome !== 'reached') {
          toast.error(t(JUMP_FAILURE_KEY[outcome]))
          return
        }
        locateTime(targetIso)
        setTimeJumpOpen(false)
      } finally {
        setJumping(false)
      }
    },
    [historyBacktrack, locateTime, t],
  )

  // ---- 搜索（把既有 xterm 搜索平移到行模型）----
  const [internalSearchOpen, setInternalSearchOpen] = useState(false)
  const searchVisible = searchOpen ?? internalSearchOpen
  const [searchQuery, setSearchQuery] = useState('')
  const [searchCurrentIndex, setSearchCurrentIndex] = useState(0)
  const searchInputRef = useRef<HTMLInputElement>(null)

  const matches = useMemo(
    () => findConsoleMatches(visibleLines, searchVisible ? searchQuery : ''),
    [visibleLines, searchQuery, searchVisible],
  )
  // 命中集随新日志涌入而变长，游标可能越界；钳制而不重置，使当前项尽量原地不动。
  const currentIndex = matches.length > 0 ? Math.min(searchCurrentIndex, matches.length - 1) : 0
  const currentMatch: ConsoleSearchMatch | undefined = matches[currentIndex]

  const setSearchVisible = useCallback(
    (open: boolean) => {
      if (searchOpen === undefined) setInternalSearchOpen(open)
      onSearchOpenChange?.(open)
    },
    [onSearchOpenChange, searchOpen],
  )

  useLayoutEffect(() => {
    if (!searchVisible) return
    searchInputRef.current?.focus()
    searchInputRef.current?.select()
  }, [searchVisible])

  const moveSearchMatch = (delta: number) => {
    if (matches.length === 0) return
    setSearchCurrentIndex((currentIndex + delta + matches.length) % matches.length)
  }

  const handleSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      setSearchVisible(false)
    } else if (event.key === 'Enter') {
      event.preventDefault()
      moveSearchMatch(event.shiftKey ? -1 : 1)
    }
  }

  // ---- 右键菜单 ----
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  // 菜单开启时按实际尺寸把落点收进视口，贴右/下边缘时向左/上收，避免溢出屏幕看不全。
  const [menuPos, setMenuPos] = useState<{ left: number; top: number } | null>(null)
  useLayoutEffect(() => {
    if (!menu || !menuRef.current) {
      setMenuPos(null)
      return
    }
    const { offsetWidth: w, offsetHeight: h } = menuRef.current
    const margin = 8
    setMenuPos({
      left: Math.max(margin, Math.min(menu.x, window.innerWidth - w - margin)),
      top: Math.max(margin, Math.min(menu.y, window.innerHeight - h - margin)),
    })
  }, [menu])

  /**
   * 全量文本一律取 `raw`：用户要的是原始日志，不是我们拆列重排过的（spec §1.1）。
   * 取 allLines 而非仅内存缓冲：用户刚往上回溯出来的历史也在屏上，「复制全部」漏掉它
   * 只会让人以为复制失败了（FR-419 引入历史层后的必然口径）。
   */
  const allText = () => consoleLinesToText(allLines)

  /**
   * 复制并给回执（FR-417 硬约束）：成功/失败都 toast，不 void 掉返回值。
   * 面板常跑在 `http://<LAN-IP>:50100` 非安全上下文，copyToClipboard 内含 execCommand 兜底，
   * 但兜底也可能失败——静默失败会让用户以为复制成功、去粘贴才发现是空的。
   */
  const notifyCopy = async (text: string) => {
    if (!text) {
      toast.error(t('instanceDetail.consoleCopyNothing'))
      return
    }
    const ok = await copyToClipboard(text)
    if (ok) toast.success(t('common.copied'))
    else toast.error(t('common.copyFailed'))
  }

  const copySelection = () => {
    // 两种选区并存（FR-418）：跨行走锚点选区（区间文本由输出区从行缓冲取，屏幕外的行
    // 没有 DOM 节点故拿不到，见 ADR-086 代价 2），同一行内仍是原生字符级选区。
    // 锚点选区一旦成立就会关掉原生选区，故它优先；为空时再退回读原生选区。
    void notifyCopy(anchorSelectionRef.current || (window.getSelection()?.toString() ?? ''))
  }

  /** 复制当前可见屏（FR-417）：行取自输出区的滚动位置反算，不含 overscan。 */
  const copyViewport = () => {
    void notifyCopy(consoleLinesToText(outputRef.current?.getVisibleLines() ?? []))
  }

  /** 仅复制错误行（FR-417）：ERROR 级 + 其完整堆栈块，不含命令回显/系统提示。 */
  const copyErrors = () => {
    void notifyCopy(consoleLinesToText(consoleErrorLines(allLines)))
  }

  const saveLog = () => {
    const text = allText()
    if (!text) {
      toast.error(t('instanceDetail.consoleCopyNothing'))
      return
    }
    const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `terminal-${instanceId}.log`
    a.click()
    URL.revokeObjectURL(url)
    toast.success(t('instanceDetail.consoleSavedLog'))
  }

  const handleMenuKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('[role="menuitem"]'))
    const current = document.activeElement as HTMLElement | null
    const index = Math.max(0, items.findIndex((item) => item === current))
    if (event.key === 'Escape') {
      event.preventDefault()
      setMenu(null)
    } else if (event.key === 'ArrowDown') {
      event.preventDefault()
      items[(index + 1) % items.length]?.focus()
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      items[(index - 1 + items.length) % items.length]?.focus()
    }
  }

  const openMenu = (event: MouseEvent<HTMLDivElement>) => {
    event.preventDefault()
    setMenu({ x: event.clientX, y: event.clientY })
  }

  if (isLoading) {
    return (
      <div className="flex h-full min-h-[400px] w-full items-center justify-center rounded-md bg-[#1a1b26]">
        <div className="flex items-center gap-2 text-sm text-gray-400">
          <div className="h-4 w-4 animate-spin rounded-full border-2 border-gray-400 border-t-transparent" />
          {t('instanceDetail.connecting')}
        </div>
      </div>
    )
  }

  return (
    <div className="relative flex h-full min-h-[320px] w-full flex-col">
      <div className="relative flex min-h-0 flex-1 gap-0">
        {searchVisible && (
          <div
            role="search"
            aria-label={t('instanceDetail.terminalSearchOpen')}
            className="absolute left-2 top-2 z-20 flex max-w-[calc(100%-4rem)] items-center gap-2 rounded-md border border-white/10 bg-[#1f2030] px-2 py-1 text-xs text-gray-200 shadow-lg"
          >
            <input
              ref={searchInputRef}
              type="search"
              value={searchQuery}
              onChange={(event) => {
                setSearchQuery(event.target.value)
                setSearchCurrentIndex(0)
              }}
              onKeyDown={handleSearchKeyDown}
              aria-label={t('instanceDetail.terminalSearchInput')}
              placeholder={t('instanceDetail.terminalSearchPlaceholder')}
              className="h-7 w-52 max-w-[50vw] rounded border border-white/10 bg-[#16161e] px-2 text-xs text-gray-100 outline-none focus:border-primary"
            />
            <span role="status" aria-live="polite" className="whitespace-nowrap text-gray-400">
              {searchQuery.trim()
                ? t('instanceDetail.terminalSearchPosition', {
                    current: matches.length > 0 ? currentIndex + 1 : 0,
                    count: matches.length,
                  })
                : t('instanceDetail.terminalSearchReady')}
            </span>
            <button
              type="button"
              onClick={() => moveSearchMatch(-1)}
              disabled={matches.length === 0}
              aria-label={t('instanceDetail.terminalSearchPrevious')}
              className="rounded px-1.5 py-0.5 text-gray-300 hover:bg-white/10 hover:text-gray-100 disabled:cursor-not-allowed disabled:opacity-40"
            >
              ↑
            </button>
            <button
              type="button"
              onClick={() => moveSearchMatch(1)}
              disabled={matches.length === 0}
              aria-label={t('instanceDetail.terminalSearchNext')}
              className="rounded px-1.5 py-0.5 text-gray-300 hover:bg-white/10 hover:text-gray-100 disabled:cursor-not-allowed disabled:opacity-40"
            >
              ↓
            </button>
            <button
              type="button"
              onClick={() => setSearchVisible(false)}
              aria-label={t('instanceDetail.terminalSearchClose')}
              className="rounded px-1.5 py-0.5 text-gray-400 hover:bg-white/10 hover:text-gray-100"
            >
              x
            </button>
          </div>
        )}

        <div className="flex min-w-0 flex-1 flex-col">
          {/* 观察工具条（FR-417）：级别过滤 + 复制范围。压到 py-0.5 / 11px——
              FR-412/422 刚把纵向空间省出来，新增一行必须细到几乎不占地方。 */}
          <div className="flex flex-none items-center gap-1 border-b border-white/10 bg-[#16161e] px-2 py-0.5 text-[11px] text-gray-400">
            <span className="shrink-0">{t('instanceDetail.consoleFilterLabel')}</span>
            {/* 分段控件容器：三段过滤收进一个凹槽，选中段浮起——替代原先散装文字钮。 */}
            <div role="group" aria-label={t('instanceDetail.consoleFilterLabel')} className="flex shrink-0 items-center gap-0.5 rounded-md bg-white/[0.06] p-0.5">
              {LEVEL_FILTERS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  onClick={() => setLevelFilter(option.value)}
                  aria-pressed={levelFilter === option.value}
                  className={cn(
                    'rounded px-2 py-0.5 font-medium transition-colors',
                    levelFilter === option.value ? 'bg-white/15 text-gray-100' : 'text-gray-400 hover:text-gray-200',
                  )}
                >
                  {t(option.labelKey)}
                </button>
              ))}
            </div>
            {levelFilter !== 'all' && (
              // 过滤生效时明示「隐藏了多少」：否则用户以为服务端安静了下来。
              <span className="shrink-0 text-gray-500">
                {t('instanceDetail.consoleFilterHidden', { count: allLines.length - visibleLines.length })}
              </span>
            )}
            {/* 自动换行开关（可用性增强）：长行折行显示，宽表格式的堆栈在窄 pane 里不再横向滚。 */}
            <button
              type="button"
              onClick={() => setWrapText((v) => !v)}
              aria-pressed={wrapText}
              title={t('instanceDetail.consoleWrapToggle')}
              className={cn(
                'flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 transition-colors',
                wrapText ? 'bg-white/15 text-gray-100' : 'hover:bg-white/10 hover:text-gray-200',
              )}
            >
              <WrapText className="size-3" />
              {t('instanceDetail.consoleWrapToggle')}
            </button>
            {/* 命令历史开关（可用性修复）：原先是 absolute right-1 top-1 的悬浮钮，
                和工具条右侧的复制菜单重叠（窄 pane 必撞）——收编进工具条成为正常 flex 项。 */}
            <button
              type="button"
              onClick={() => setDrawerOpen((v) => !v)}
              aria-expanded={drawerOpen}
              title={t('instanceDetail.terminalHistory')}
              className={cn(
                'flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 transition-colors',
                drawerOpen ? 'bg-white/15 text-gray-100' : 'hover:bg-white/10 hover:text-gray-200',
              )}
            >
              <History className="size-3" />
              {t('instanceDetail.terminalHistory')}
            </button>
            <button
              type="button"
              onClick={(event) => {
                const rect = event.currentTarget.getBoundingClientRect()
                setMenu({ x: rect.left, y: rect.bottom })
              }}
              aria-label={t('instanceDetail.consoleCopyMenu')}
              className="ml-auto flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 hover:bg-white/10 hover:text-gray-200"
            >
              <Copy className="size-3" />
              {t('instanceDetail.consoleCopyMenu')}
            </button>
          </div>
          <ConsoleOutputView
            ref={outputRef}
            lines={visibleLines}
            droppedCount={droppedCount}
            fontSize={fontSize}
            matches={matches}
            currentMatch={currentMatch}
            filterActive={levelFilter !== 'all'}
            onContextMenu={openMenu}
            onSelectionChange={(text) => {
              anchorSelectionRef.current = text
            }}
            logNamePrefix={`terminal-${instanceId}`}
            onLoadEarlier={historyBacktrack.loadEarlier}
            history={{
              loading: historyBacktrack.loading,
              exhausted: historyBacktrack.exhausted,
              rowCount: historyBacktrack.rowCount,
              memoryCount: lines.length,
              oldestTime: historyBacktrack.oldestTime,
              error: historyBacktrack.error,
            }}
            jumpTarget={jumpTarget}
            historyActions={
              <>
                <button
                  type="button"
                  onClick={jumpToStartup}
                  disabled={jumping}
                  className="rounded px-1.5 py-0.5 hover:bg-white/10 hover:text-gray-200 disabled:opacity-50"
                >
                  {t('instanceDetail.consoleJumpToStartup')}
                </button>
                <button
                  type="button"
                  onClick={() => setTimeJumpOpen(true)}
                  disabled={jumping}
                  className="rounded px-1.5 py-0.5 hover:bg-white/10 hover:text-gray-200 disabled:opacity-50"
                >
                  {t('instanceDetail.consoleJumpToTime')}
                </button>
              </>
            }
            immersive={immersive}
            wrap={wrapText}
          />
        </div>

        {drawerOpen && (
          <div className="flex w-60 flex-col border-l border-white/10 bg-[#141420]">
            <div className="flex flex-none items-center justify-between border-b border-white/10 px-3 py-2">
              <span className="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-gray-400">
                <History className="size-3" />
                {t('instanceDetail.terminalHistoryTitle')}
              </span>
              <span className="rounded-full bg-white/10 px-1.5 font-mono text-[10px] text-gray-400">{history.length}</span>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto p-1.5">
              {history.length === 0 ? (
                <div className="p-2 text-xs text-gray-500">{t('instanceDetail.terminalHistoryEmpty')}</div>
              ) : (
                [...history].reverse().map((cmd, i) => (
                  <button
                    key={`${i}-${cmd}`}
                    type="button"
                    onClick={() => submitCommand(cmd)}
                    aria-label={cmd}
                    className="group flex w-full items-center gap-2 rounded px-2 py-1.5 text-left transition-colors hover:bg-white/10"
                    title={cmd}
                  >
                    {/* 序号 = 最近使用排序（1=最近），等宽数字列让长短命令对齐 */}
                    <span className="w-4 shrink-0 text-right font-mono text-[10px] text-gray-600 group-hover:text-gray-400">
                      {i + 1}
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono text-xs text-gray-200">{cmd}</span>
                    <CornerDownLeft className="size-3 shrink-0 text-gray-600 opacity-0 transition-opacity group-hover:opacity-100" />
                  </button>
                ))
              )}
            </div>
          </div>
        )}
      </div>

      <ConsoleCommandBar
        disabled={readOnly}
        disabledReason={readOnly ? disabledReason : undefined}
        action={disabledAction}
        onSubmit={submitCommand}
        history={history}
        players={players}
        fontSize={fontSize}
        immersive={immersive}
      />

      {/* 右键菜单：portal 到 body——控制台/路由壳带 transform（will-change/matrix），
          fixed 的包含块会被劫持为该祖先，视口坐标整体偏移；出壳后 fixed 才真正相对视口。 */}
      {menu &&
        createPortal(
          <>
            <div
              className="fixed inset-0 z-20"
              onClick={() => setMenu(null)}
              onContextMenu={(e) => {
                e.preventDefault()
                setMenu(null)
              }}
            />
            <div
              ref={menuRef}
              role="menu"
              aria-label={t('instanceDetail.terminalMenu')}
              tabIndex={-1}
              onKeyDown={handleMenuKeyDown}
              className="fixed z-30 min-w-36 rounded-md border border-white/10 bg-[#1f2030] py-1 text-sm text-gray-200 shadow-lg"
              style={{ left: menuPos?.left ?? menu.x, top: menuPos?.top ?? menu.y }}
            >
              <button
                type="button"
                role="menuitem"
                className="block w-full px-3 py-1 text-left hover:bg-white/10"
                onClick={() => {
                  copySelection()
                  setMenu(null)
                }}
              >
                {t('instanceDetail.terminalCopySelection')}
              </button>
              {/* 复制范围（FR-417）：排障时要贴的往往不是「全部」——可见屏对应「我正看着的这段」，
                  仅错误行对应「把异常发给别人」，两者都比 5000 行全量更常用。 */}
              <button
                type="button"
                role="menuitem"
                className="block w-full px-3 py-1 text-left hover:bg-white/10"
                onClick={() => {
                  copyViewport()
                  setMenu(null)
                }}
              >
                {t('instanceDetail.consoleCopyViewport')}
              </button>
              <button
                type="button"
                role="menuitem"
                className="block w-full px-3 py-1 text-left hover:bg-white/10"
                onClick={() => {
                  void notifyCopy(allText())
                  setMenu(null)
                }}
              >
                {t('instanceDetail.consoleCopyBuffer')}
              </button>
              <button
                type="button"
                role="menuitem"
                className="block w-full px-3 py-1 text-left hover:bg-white/10"
                onClick={() => {
                  copyErrors()
                  setMenu(null)
                }}
              >
                {t('instanceDetail.consoleCopyErrors')}
              </button>
              <button
                type="button"
                role="menuitem"
                className="block w-full px-3 py-1 text-left hover:bg-white/10"
                onClick={() => {
                  saveLog()
                  setMenu(null)
                }}
              >
                {t('instanceDetail.consoleSaveLog')}
              </button>
              <div className="my-1 border-t border-white/10" />
              <button
                type="button"
                role="menuitem"
                className="block w-full px-3 py-1 text-left hover:bg-white/10"
                onClick={() => {
                  terminalSessionManager.clearLines(instanceId)
                  setMenu(null)
                }}
              >
                {t('instanceDetail.terminalClear')}
              </button>
            </div>
          </>,
          document.body,
        )}

      {/* 「跳到时间…」（FR-419）：表单交互一律模态承载（.claude/rules/ui-modals.md），
          不在页面里就地展开一段输入框把日志区顶开。单字段短表单，无需滚动壳。 */}
      <Dialog open={timeJumpOpen} onOpenChange={setTimeJumpOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>{t('instanceDetail.consoleJumpTimeTitle')}</DialogTitle>
            <DialogDescription>{t('instanceDetail.consoleJumpTimeHint')}</DialogDescription>
          </DialogHeader>
          <form
            onSubmit={(event) => {
              event.preventDefault()
              // datetime-local 给的是无时区本地时间串；经 Date 解析成本地时刻再转 ISO，
              // 与后端按 RFC3339 比较的语义对齐（直接把裸串当 ISO 会偏一个时区）。
              const parsed = new Date(timeJumpValue)
              if (Number.isNaN(parsed.getTime())) {
                toast.error(t('instanceDetail.consoleJumpTimeInvalid'))
                return
              }
              void jumpToTime(parsed.toISOString())
            }}
          >
            <label className="block space-y-1 text-sm">
              <span className="text-muted-foreground">{t('instanceDetail.consoleJumpTimeLabel')}</span>
              <input
                type="datetime-local"
                step="1"
                value={timeJumpValue}
                onChange={(event) => setTimeJumpValue(event.target.value)}
                aria-label={t('instanceDetail.consoleJumpTimeLabel')}
                className="h-9 w-full rounded-md border bg-background px-2 text-sm outline-none focus:border-primary"
              />
            </label>
            <DialogFooter className="mt-4">
              <Button type="button" variant="outline" size="sm" onClick={() => setTimeJumpOpen(false)}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" size="sm" disabled={jumping || !timeJumpValue}>
                {jumping ? t('instanceDetail.consoleJumpSearching') : t('instanceDetail.consoleJumpConfirm')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}
