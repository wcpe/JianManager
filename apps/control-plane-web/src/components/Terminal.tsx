import { useEffect, useLayoutEffect, useRef, useCallback, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import type { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useDirectorRender } from '@/lib/director-render'
import { terminalSessionManager } from '@/lib/terminal-session-manager'
import { copyToClipboard } from '@/lib/clipboard'
import { useTranslation } from 'react-i18next'

interface TerminalComponentProps {
  instanceId: string
  /**
   * 拉取一次性终端连接凭据（wsUrl + token）。**每次连接前现取**：
   * 一次性 token 首连即被 CP 消费失效，重连必须重取新 token，否则复用会 401（FR-140）。
   */
  fetchToken?: () => Promise<{ wsUrl: string; token: string }>
  readOnly?: boolean
  /** token 正在加载中，显示占位而非尝试连接 */
  isLoading?: boolean
  /** 终端字号（由外层工具栏控制）。 */
  fontSize?: number
  /** 外层工具栏控制的搜索框开关。 */
  searchOpen?: boolean
  /** 搜索框开关变更回调。 */
  onSearchOpenChange?: (open: boolean) => void
  /**
   * 会话保活（FR-295，ADR-067）：true=卸载（含 Activity 隐藏）只 detach，
   * 连接与缓冲由连接管理器常驻（控制台 keep-alive 宿主负责最终释放）；
   * false（默认）=独立表面语义，卸载即释放会话（未被控制台热集 pin 时）。
   */
  persistSession?: boolean
}

// 常用 MC/Paper 控制台命令，用于 Tab 补全（服务端控制台非 PTY，无原生补全）。
const MC_COMMANDS = [
  'advancement', 'attribute', 'ban', 'ban-ip', 'banlist', 'bossbar', 'clear', 'clone', 'damage',
  'data', 'datapack', 'debug', 'defaultgamemode', 'deop', 'difficulty', 'effect', 'enchant',
  'execute', 'experience', 'fill', 'fillbiome', 'forceload', 'function', 'gamemode', 'gamerule',
  'give', 'help', 'item', 'jfr', 'kick', 'kill', 'list', 'locate', 'loot', 'me', 'msg', 'op',
  'pardon', 'pardon-ip', 'particle', 'perf', 'place', 'playsound', 'plugins', 'random', 'recipe',
  'reload', 'return', 'ride', 'rotate', 'save-all', 'save-off', 'save-on', 'say', 'schedule',
  'scoreboard', 'seed', 'setblock', 'setidletimeout', 'setworldspawn', 'spawnpoint', 'spectate',
  'spreadplayers', 'stop', 'stopsound', 'summon', 'tag', 'team', 'teammsg', 'teleport', 'tell',
  'tellraw', 'tick', 'time', 'timings', 'title', 'tp', 'tps', 'transfer', 'trigger', 'version',
  'w', 'weather', 'whitelist', 'worldborder', 'xp',
]

// 参数为玩家的命令——补全时给出在线玩家名 + 选择器。
const PLAYER_ARG_COMMANDS = new Set([
  'kick', 'ban', 'pardon', 'op', 'deop', 'tp', 'teleport', 'gamemode', 'give', 'tell', 'msg', 'w',
  'kill', 'spectate', 'whitelist', 'clear', 'effect', 'enchant', 'experience', 'xp', 'title',
  'spawnpoint', 'teammsg',
])
const SELECTORS = ['@a', '@p', '@r', '@e', '@s']

type TerminalSearchMatch = {
  line: number
  start: number
  end: number
}

const findSearchMatches = (lines: string[], query: string): TerminalSearchMatch[] => {
  const needle = query.trim().toLocaleLowerCase()
  if (!needle) return []
  const matches: TerminalSearchMatch[] = []
  for (let line = 0; line < lines.length; line++) {
    const haystack = lines[line].toLocaleLowerCase()
    let index = haystack.indexOf(needle)
    while (index >= 0) {
      matches.push({ line, start: index, end: index + needle.length })
      index = haystack.indexOf(needle, index + needle.length)
    }
  }
  return matches
}

const clearSearchMarks = (root: HTMLElement) => {
  root.querySelectorAll('mark[data-terminal-search-match="true"]').forEach((mark) => {
    mark.replaceWith(document.createTextNode(mark.textContent ?? ''))
  })
  root.normalize()
}

const highlightTextNode = (
  node: Text,
  nodeStart: number,
  matches: TerminalSearchMatch[],
  current: TerminalSearchMatch | undefined,
) => {
  const text = node.nodeValue ?? ''
  const fragment = document.createDocumentFragment()
  let cursor = 0
  for (const match of matches) {
    const start = Math.max(match.start - nodeStart, 0)
    const end = Math.min(match.end - nodeStart, text.length)
    if (end <= cursor || start >= text.length) continue
    if (start > cursor) fragment.append(document.createTextNode(text.slice(cursor, start)))
    const mark = document.createElement('mark')
    mark.dataset.terminalSearchMatch = 'true'
    if (current && match.line === current.line && match.start === current.start && match.end === current.end) {
      mark.dataset.terminalSearchCurrent = 'true'
      mark.className = 'rounded bg-amber-300 px-0.5 text-black ring-1 ring-amber-100'
    } else {
      mark.className = 'rounded bg-yellow-300/60 px-0.5 text-black'
    }
    mark.textContent = text.slice(start, end)
    fragment.append(mark)
    cursor = end
  }
  if (cursor < text.length) fragment.append(document.createTextNode(text.slice(cursor)))
  if (fragment.childNodes.length > 0) node.replaceWith(fragment)
}

const highlightRowMatches = (
  row: HTMLElement,
  matches: TerminalSearchMatch[],
  current: TerminalSearchMatch | undefined,
) => {
  const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT)
  const nodes: Array<{ node: Text; start: number; end: number }> = []
  let offset = 0
  while (walker.nextNode()) {
    const node = walker.currentNode as Text
    const text = node.nodeValue ?? ''
    nodes.push({ node, start: offset, end: offset + text.length })
    offset += text.length
  }
  for (const { node, start, end } of nodes) {
    const nodeMatches = matches.filter((match) => match.start < end && match.end > start)
    if (nodeMatches.length > 0) highlightTextNode(node, start, nodeMatches, current)
  }
}

/**
 * 终端渲染壳（FR-295 改造，ADR-067）：xterm 实例与 WS 连接常驻
 * {@link terminalSessionManager}，本组件只负责 attach 渲染、输入处理与
 * 搜索/历史/右键菜单等交互；卸载（含 `<Activity>` 隐藏）不再断连。
 */
export default function TerminalComponent({
  instanceId,
  fetchToken,
  readOnly = false,
  isLoading = false,
  fontSize = 14,
  searchOpen,
  onSearchOpenChange,
  persistSession = false,
}: TerminalComponentProps) {
  const { t } = useTranslation()
  const numericId = Number(instanceId)
  const terminalRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const lineBufRef = useRef('')
  // readOnly 用 ref：实例状态变化只切换是否允许输入，不重建/不重连——停服时保持连接看关服日志。
  const readOnlyRef = useRef(readOnly)
  useEffect(() => {
    readOnlyRef.current = readOnly
  }, [readOnly])

  // 导播台节流（FR-168 / ADR-035）：非激活场景的终端 WS 保活但**暂停 xterm 重绘**——
  // 输出由管理器累积，切回激活时一次性 flush。无 Provider 时恒激活（FR-166/167 不变）。
  const { active: directorActive } = useDirectorRender()

  // 命令历史（ref 供输入处理用，state 供右侧抽屉渲染）
  const historyRef = useRef<string[]>([])
  const histIdxRef = useRef(-1)
  const draftRef = useRef('')
  const [history, setHistory] = useState<string[]>([])
  // 在线玩家（解析输出维护），用于玩家名补全
  const onlinePlayersRef = useRef<Set<string>>(new Set())
  const parseBufRef = useRef('')
  // 右键菜单
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  // 右键菜单最终落点（钳制进视口后）。菜单开启时按实际尺寸把落点收进视口，
  // 贴右/下边缘时向左/上收，避免菜单溢出屏幕看不全（FIX：原直接用 clientX/Y 无边界钳制）。
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
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [internalSearchOpen, setInternalSearchOpen] = useState(false)
  const searchVisible = searchOpen ?? internalSearchOpen
  const [searchQuery, setSearchQuery] = useState('')
  const searchQueryRef = useRef('')
  const [searchMatches, setSearchMatches] = useState<TerminalSearchMatch[]>([])
  const [searchCurrentIndex, setSearchCurrentIndex] = useState(0)
  const searchMatchesRef = useRef<TerminalSearchMatch[]>([])
  const searchCurrentIndexRef = useRef(0)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const searchHighlightTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const searchVisibleRef = useRef(searchVisible)
  const setSearchVisibleRef = useRef<(open: boolean) => void>(() => {})
  const refreshSearchRef = useRef<(query: string, targetIndex?: number) => void>(() => {})
  const scheduleSearchHighlightRef = useRef<() => void>(() => {})
  const setSearchVisible = useCallback((open: boolean) => setSearchVisibleRef.current(open), [])

  useEffect(() => {
    searchVisibleRef.current = searchVisible
  }, [searchVisible])

  useEffect(() => {
    searchQueryRef.current = searchQuery
  }, [searchQuery])

  // 把一行命令下发并入历史
  const submitLine = useCallback(() => {
    const line = lineBufRef.current
    terminalSessionManager.send(numericId, { type: 'stdin', instanceId, data: line })
    termRef.current?.write('\r\n')
    if (line.trim()) {
      historyRef.current = [...historyRef.current, line].slice(-200)
      setHistory(historyRef.current)
    }
    histIdxRef.current = -1
    lineBufRef.current = ''
  }, [instanceId, numericId])

  // 用 newLine 替换当前输入行（历史导航/插入用）
  const replaceLine = useCallback((newLine: string) => {
    const term = termRef.current
    if (!term) return
    const cur = lineBufRef.current
    for (let i = 0; i < cur.length; i++) term.write('\b \b')
    lineBufRef.current = newLine
    term.write(newLine)
  }, [])

  // 把命令填入输入行（抽屉/菜单点击用）
  const insertCommand = useCallback((cmd: string) => {
    replaceLine(cmd)
    termRef.current?.focus()
  }, [replaceLine])

  // 复制当前选区到剪贴板
  const copySelection = useCallback(() => {
    const sel = termRef.current?.getSelection()
    if (sel) {
      void copyToClipboard(sel)
      termRef.current?.clearSelection()
    }
  }, [])

  const pasteClipboard = useCallback(async () => {
    try {
      const text = await navigator.clipboard?.readText()
      if (text) {
        const oneLine = text.replace(/[\r\n]+/g, ' ')
        lineBufRef.current += oneLine
        termRef.current?.write(oneLine)
      }
    } catch { /* 剪贴板不可用时忽略 */ }
  }, [])

  // 全选终端可见+滚动缓冲区内容
  const selectAll = useCallback(() => termRef.current?.selectAll(), [])

  // 取整个缓冲区文本（含滚动历史）
  const getAllText = useCallback(() => {
    const term = termRef.current
    if (!term) return ''
    term.selectAll()
    const text = term.getSelection()
    term.clearSelection()
    return text
  }, [])

  const getSearchableLines = useCallback(() => {
    const buffer = termRef.current?.buffer.active
    const lines: string[] = []
    if (buffer) {
      for (let i = 0; i < buffer.length; i++) {
        lines.push(buffer.getLine(i)?.translateToString(true) ?? '')
      }
    }
    if (lineBufRef.current) lines.push(lineBufRef.current)
    return lines
  }, [])

  const clearVisibleSearchHighlights = useCallback(() => {
    const root = terminalRef.current
    if (!root) return
    clearSearchMarks(root)
  }, [])

  useEffect(() => {
    scheduleSearchHighlightRef.current = () => {
      if (searchHighlightTimerRef.current) clearTimeout(searchHighlightTimerRef.current)
      searchHighlightTimerRef.current = setTimeout(() => {
        const root = terminalRef.current
        const term = termRef.current
        if (!root || !term) return
        clearSearchMarks(root)
        const matches = searchMatchesRef.current
        const query = searchQueryRef.current.trim()
        if (!query || matches.length === 0 || !searchVisibleRef.current) return
        const current = matches[searchCurrentIndexRef.current]
        const viewportY = term.buffer.active.viewportY ?? 0
        const rows = root.querySelectorAll<HTMLElement>('.xterm-rows > div')
        rows.forEach((row, rowIndex) => {
          const line = viewportY + rowIndex
          const rowMatches = matches.filter((match) => match.line === line)
          if (rowMatches.length > 0) highlightRowMatches(row, rowMatches, current)
        })
      }, 0)
    }
  }, [])

  const refreshSearch = useCallback((query: string, targetIndex = 0) => {
    const matches = findSearchMatches(getSearchableLines(), query)
    const currentIndex = matches.length > 0 ? Math.max(0, Math.min(targetIndex, matches.length - 1)) : 0
    searchMatchesRef.current = matches
    searchCurrentIndexRef.current = currentIndex
    setSearchMatches(matches)
    setSearchCurrentIndex(currentIndex)
    if (matches.length > 0) termRef.current?.scrollToLine(matches[currentIndex].line)
    scheduleSearchHighlightRef.current()
  }, [getSearchableLines])

  useEffect(() => {
    refreshSearchRef.current = refreshSearch
  }, [refreshSearch])

  useEffect(() => {
    setSearchVisibleRef.current = (open: boolean) => {
      if (searchOpen === undefined) setInternalSearchOpen(open)
      onSearchOpenChange?.(open)
      if (open) refreshSearchRef.current(searchQueryRef.current, searchCurrentIndexRef.current)
    }
  }, [onSearchOpenChange, searchOpen])

  useEffect(() => {
    if (!searchVisible) {
      clearVisibleSearchHighlights()
      return
    }
    searchInputRef.current?.focus()
    searchInputRef.current?.select()
    refreshSearchRef.current(searchQueryRef.current, searchCurrentIndexRef.current)
  }, [clearVisibleSearchHighlights, searchVisible])

  const closeSearch = useCallback(() => {
    setSearchVisible(false)
    termRef.current?.focus()
  }, [setSearchVisible])

  const updateSearchQuery = useCallback((query: string) => {
    setSearchQuery(query)
    searchQueryRef.current = query
    refreshSearch(query, 0)
  }, [refreshSearch])

  const moveSearchMatch = useCallback((delta: number) => {
    const matches = searchMatchesRef.current
    if (matches.length === 0) return
    const nextIndex = (searchCurrentIndexRef.current + delta + matches.length) % matches.length
    searchCurrentIndexRef.current = nextIndex
    setSearchCurrentIndex(nextIndex)
    termRef.current?.scrollToLine(matches[nextIndex].line)
    scheduleSearchHighlightRef.current()
  }, [])

  const handleSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      closeSearch()
    } else if (event.key === 'Enter') {
      event.preventDefault()
      moveSearchMatch(event.shiftKey ? -1 : 1)
    }
  }

  // 复制全部日志到剪贴板
  const copyAll = useCallback(() => {
    const text = getAllText()
    if (text) void copyToClipboard(text)
  }, [getAllText])

  // 保存当前日志为本地文件
  const saveLog = useCallback(() => {
    const text = getAllText()
    if (!text) return
    const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `terminal-${instanceId}.log`
    a.click()
    URL.revokeObjectURL(url)
  }, [getAllText, instanceId])

  // 会话订阅（FR-295，ADR-067）：mount 时 acquire+attach 常驻 xterm，注册输入/输出处理；
  // 卸载（含 Activity 隐藏）只解绑渲染层，连接与滚动缓冲留在管理器（persistSession=false
  // 的独立表面在此释放会话，维持「未挂载卡不建 WS」的旧资源语义）。
  useEffect(() => {
    const container = terminalRef.current
    if (!container || isLoading || !fetchToken) return

    terminalSessionManager.acquire(numericId, fetchToken, { fontSize })
    terminalSessionManager.attach(numericId, container)
    const term = terminalSessionManager.getTerm(numericId)
    if (!term) return
    termRef.current = term

    term.attachCustomKeyEventHandler((event) => {
      if (event.type === 'keydown' && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'f') {
        event.preventDefault()
        setSearchVisibleRef.current(true)
        return false
      }
      if (event.type === 'keydown' && event.key === 'Escape' && searchVisibleRef.current) {
        event.preventDefault()
        setSearchVisibleRef.current(false)
        return false
      }
      return true
    })
    const searchScrollDisposable = term.onScroll(() => {
      if (searchVisibleRef.current && searchQueryRef.current.trim()) scheduleSearchHighlightRef.current()
    })

    // Tab 补全：命令首词 / 玩家命令的玩家名 + 选择器
    const complete = () => {
      const buf = lineBufRef.current
      const parts = buf.split(/\s+/)
      const cur = parts[parts.length - 1]
      let cands: string[]
      if (parts.length <= 1) {
        cands = MC_COMMANDS
      } else if (PLAYER_ARG_COMMANDS.has(parts[0])) {
        // 已输入 @ 才补选择器；否则只补在线玩家名（据终端输出实时维护，无人在线则不补）
        cands = cur.startsWith('@') ? SELECTORS : [...onlinePlayersRef.current]
      } else {
        return
      }
      const matches = cands.filter((c) => c.startsWith(cur))
      if (matches.length === 1) {
        const rest = matches[0].slice(cur.length) + ' '
        lineBufRef.current += rest
        term.write(rest)
      } else if (matches.length > 1) {
        term.write('\r\n' + matches.join('  ') + '\r\n' + buf)
      }
    }

    const dataDisposable = term.onData((data) => {
      if (readOnlyRef.current) return // 实例非运行：忽略输入但保持连接
      // 整体匹配的转义/控制序列
      if (data === '\x1b[A') { // ↑ 上一条历史
        const h = historyRef.current
        if (h.length === 0) return
        if (histIdxRef.current === -1) { draftRef.current = lineBufRef.current; histIdxRef.current = h.length }
        if (histIdxRef.current > 0) { histIdxRef.current--; replaceLine(h[histIdxRef.current]) }
        return
      }
      if (data === '\x1b[B') { // ↓ 下一条历史
        if (histIdxRef.current === -1) return
        const h = historyRef.current
        if (histIdxRef.current < h.length - 1) { histIdxRef.current++; replaceLine(h[histIdxRef.current]) }
        else { histIdxRef.current = -1; replaceLine(draftRef.current) }
        return
      }
      if (data === '\x03') { // Ctrl+C：有选区则复制，否则忽略（MC 控制台无中断语义）
        copySelection()
        return
      }
      for (let i = 0; i < data.length; i++) {
        const ch = data[i]
        if (ch === '\r' || ch === '\n') {
          if (ch === '\n' && i > 0 && data[i - 1] === '\r') continue
          submitLine()
        } else if (ch === '\x7f' || ch === '\b') {
          if (lineBufRef.current.length > 0) {
            lineBufRef.current = lineBufRef.current.slice(0, -1)
            term.write('\b \b')
          }
        } else if (ch === '\t') {
          complete()
        } else if (ch >= ' ') {
          lineBufRef.current += ch
          term.write(ch)
        }
      }
    })

    // 输出订阅：解析在线玩家（逐完整行匹配加入/离开/list 输出）+ 搜索高亮联动。
    const unsubscribeOutput = terminalSessionManager.onOutput(numericId, (text) => {
      parseBufRef.current += text
      let nl: number
      while ((nl = parseBufRef.current.indexOf('\n')) >= 0) {
        const raw = parseBufRef.current.slice(0, nl)
        parseBufRef.current = parseBufRef.current.slice(nl + 1)
        // 去 ANSI 颜色码与 CR：Paper 控制台给玩家名套色，否则玩家名被转义码包裹导致正则匹配不到
        // eslint-disable-next-line no-control-regex -- ANSI 转义符为有意匹配
        const line = raw.replace(/\x1b\[[0-9;]*[A-Za-z]/g, '').replace(/\r/g, '')
        const join = line.match(/([A-Za-z0-9_]{1,16}) joined the game/)
        if (join) onlinePlayersRef.current.add(join[1])
        const left = line.match(/([A-Za-z0-9_]{1,16}) left the game/)
        if (left) onlinePlayersRef.current.delete(left[1])
        const list = line.match(/players online:\s*(.+)$/)
        if (list) onlinePlayersRef.current = new Set(list[1].split(/,\s*/).map((s) => s.trim()).filter(Boolean))
      }
      if (searchVisibleRef.current && searchQueryRef.current.trim()) scheduleSearchHighlightRef.current()
    })

    const handleResize = () => {
      terminalSessionManager.fit(numericId)
      terminalSessionManager.send(numericId, { type: 'resize', instanceId, cols: term.cols, rows: term.rows })
    }
    window.addEventListener('resize', handleResize)

    return () => {
      window.removeEventListener('resize', handleResize)
      if (searchHighlightTimerRef.current) clearTimeout(searchHighlightTimerRef.current)
      searchScrollDisposable.dispose()
      dataDisposable.dispose()
      unsubscribeOutput()
      terminalSessionManager.detach(numericId)
      // 独立表面（画布卡片等）卸载即释放；控制台 keep-alive 宿主下只解绑渲染。
      if (!persistSession) terminalSessionManager.release(numericId)
    }
    // 故意不依赖 readOnly/fontSize：实例状态与字号变化不重建订阅、不断连。
    // eslint-disable-next-line react-hooks/exhaustive-deps -- fontSize 仅作首建默认值，运行时经 setFontSize 调整
  }, [instanceId, numericId, isLoading, fetchToken, persistSession, submitLine, replaceLine, copySelection])

  // 导播台激活态 → 管理器暂停/恢复重绘（恢复时管理器一次性 flush 累积输出）。
  useEffect(() => {
    terminalSessionManager.setPaused(numericId, !directorActive)
  }, [directorActive, numericId])

  // 字号运行时调整：不再重建终端/重连（原实现整拆整建）。
  useEffect(() => {
    terminalSessionManager.setFontSize(numericId, fontSize)
  }, [fontSize, numericId])

  if (isLoading) {
    return (
      <div className="w-full h-full min-h-[400px] bg-[#1a1b26] rounded-md flex items-center justify-center">
        <div className="flex items-center gap-2 text-gray-400 text-sm">
          <div className="h-4 w-4 animate-spin rounded-full border-2 border-gray-400 border-t-transparent" />
          {t('instanceDetail.connecting')}
        </div>
      </div>
    )
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

  return (
    <div className="relative flex h-full min-h-[400px] w-full gap-0">
      {searchVisible && (
        <div
          role="search"
          aria-label={t('instanceDetail.terminalSearchOpen', { defaultValue: '搜索终端' })}
          className="absolute left-2 top-2 z-20 flex max-w-[calc(100%-4rem)] items-center gap-2 rounded-md border border-white/10 bg-[#1f2030] px-2 py-1 text-xs text-gray-200 shadow-lg"
        >
          <input
            ref={searchInputRef}
            type="search"
            value={searchQuery}
            onChange={(event) => updateSearchQuery(event.target.value)}
            onFocus={() => refreshSearch(searchQuery, searchCurrentIndexRef.current)}
            onKeyDown={handleSearchKeyDown}
            aria-label={t('instanceDetail.terminalSearchInput', { defaultValue: '搜索终端输入' })}
            placeholder={t('instanceDetail.terminalSearchPlaceholder', { defaultValue: '搜索终端输出' })}
            className="h-7 w-52 max-w-[50vw] rounded border border-white/10 bg-[#16161e] px-2 text-xs text-gray-100 outline-none focus:border-primary"
          />
          <span role="status" aria-live="polite" className="whitespace-nowrap text-gray-400">
            {searchQuery.trim()
              ? t('instanceDetail.terminalSearchPosition', {
                  current: searchMatches.length > 0 ? searchCurrentIndex + 1 : 0,
                  count: searchMatches.length,
                  defaultValue: '{{current}} / {{count}} matches',
                })
              : t('instanceDetail.terminalSearchReady', { defaultValue: '输入关键字搜索' })}
          </span>
          <button
            type="button"
            onClick={() => moveSearchMatch(-1)}
            disabled={searchMatches.length === 0}
            aria-label={t('instanceDetail.terminalSearchPrevious', { defaultValue: '上一条匹配' })}
            className="rounded px-1.5 py-0.5 text-gray-300 hover:bg-white/10 hover:text-gray-100 disabled:cursor-not-allowed disabled:opacity-40"
          >
            ↑
          </button>
          <button
            type="button"
            onClick={() => moveSearchMatch(1)}
            disabled={searchMatches.length === 0}
            aria-label={t('instanceDetail.terminalSearchNext', { defaultValue: '下一条匹配' })}
            className="rounded px-1.5 py-0.5 text-gray-300 hover:bg-white/10 hover:text-gray-100 disabled:cursor-not-allowed disabled:opacity-40"
          >
            ↓
          </button>
          <button
            type="button"
            onClick={closeSearch}
            aria-label={t('instanceDetail.terminalSearchClose', { defaultValue: '关闭终端搜索' })}
            className="rounded px-1.5 py-0.5 text-gray-400 hover:bg-white/10 hover:text-gray-100"
          >
            x
          </button>
        </div>
      )}
      {/* 终端区：支持鼠标拖选复制；右键弹出菜单 */}
      <div
        ref={terminalRef}
        className="min-w-0 flex-1 bg-[#1a1b26] rounded-md overflow-hidden"
        onContextMenu={(e) => { e.preventDefault(); setMenu({ x: e.clientX, y: e.clientY }) }}
      />

      {/* 历史命令抽屉切换 */}
      <button
        type="button"
        onClick={() => setDrawerOpen((v) => !v)}
        className="absolute right-1 top-1 z-10 rounded bg-white/10 px-2 py-0.5 text-xs text-gray-300 hover:bg-white/20"
        title={t('instanceDetail.terminalHistory')}
        aria-label={t('instanceDetail.terminalHistory')}
      >
        {drawerOpen ? '▶' : '◀'} {t('instanceDetail.terminalHistory')}
      </button>

      {/* 历史命令抽屉 */}
      {drawerOpen && (
        <div className="flex w-56 flex-col rounded-md border-l border-white/10 bg-[#16161e]">
          <div className="border-b border-white/10 px-3 py-2 text-xs font-medium text-gray-300">{t('instanceDetail.terminalHistoryTitle')}</div>
          <div className="min-h-0 flex-1 overflow-y-auto p-1">
            {history.length === 0 ? (
              <div className="p-2 text-xs text-gray-500">{t('instanceDetail.terminalHistoryEmpty')}</div>
            ) : (
              [...history].reverse().map((cmd, i) => (
                <button
                  key={`${i}-${cmd}`}
                  type="button"
                  onClick={() => insertCommand(cmd)}
                  className="block w-full truncate rounded px-2 py-1 text-left font-mono text-xs text-gray-300 hover:bg-white/10"
                  title={cmd}
                >
                  {cmd}
                </button>
              ))
            )}
          </div>
        </div>
      )}

      {/* 右键菜单：portal 到 body——控制台/路由壳带 transform（will-change/matrix），
          fixed 的包含块会被劫持为该祖先，视口坐标整体偏移；出壳后 fixed 才真正相对视口。 */}
      {menu && createPortal(
        <>
          <div className="fixed inset-0 z-20" onClick={() => setMenu(null)} onContextMenu={(e) => { e.preventDefault(); setMenu(null) }} />
          <div
            ref={menuRef}
            role="menu"
            aria-label={t('instanceDetail.terminalMenu')}
            tabIndex={-1}
            onKeyDown={handleMenuKeyDown}
            className="fixed z-30 min-w-36 rounded-md border border-white/10 bg-[#1f2030] py-1 text-sm text-gray-200 shadow-lg"
            style={{ left: menuPos?.left ?? menu.x, top: menuPos?.top ?? menu.y }}
          >
            <button type="button" role="menuitem" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { copySelection(); setMenu(null) }}>{t('instanceDetail.terminalCopySelection')}</button>
            <button type="button" role="menuitem" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { selectAll(); setMenu(null) }}>{t('instanceDetail.terminalSelectAll')}</button>
            <button type="button" role="menuitem" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { copyAll(); setMenu(null) }}>{t('instanceDetail.terminalCopyAll')}</button>
            <button type="button" role="menuitem" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { saveLog(); setMenu(null) }}>{t('instanceDetail.terminalSaveLog')}</button>
            <div className="my-1 border-t border-white/10" />
            <button type="button" role="menuitem" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { pasteClipboard(); setMenu(null) }}>{t('instanceDetail.terminalPaste')}</button>
            <button type="button" role="menuitem" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { termRef.current?.clear(); setMenu(null) }}>{t('instanceDetail.terminalClear')}</button>
          </div>
        </>,
        document.body,
      )}
    </div>
  )
}
