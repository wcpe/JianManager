import { useEffect, useRef, useCallback, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { useDirectorRender } from '@/lib/director-render'
import { copyToClipboard } from '@/lib/clipboard'

interface TerminalComponentProps {
  instanceId: string
  wsUrl?: string
  token?: string
  readOnly?: boolean
  /** token 正在加载中，显示占位而非尝试连接 */
  isLoading?: boolean
}

const MAX_RETRIES = 3
const BASE_RETRY_DELAY = 1000

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

export default function TerminalComponent({ instanceId, wsUrl, token, readOnly = false, isLoading = false }: TerminalComponentProps) {
  const terminalRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const cleanupRef = useRef(false)
  const retryCountRef = useRef(0)
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lineBufRef = useRef('')
  // readOnly 用 ref：实例状态变化只切换是否允许输入，不重建/不重连——停服时保持连接看关服日志。
  const readOnlyRef = useRef(readOnly)
  useEffect(() => {
    readOnlyRef.current = readOnly
  }, [readOnly])

  // 导播台节流（FR-168 / ADR-035）：非激活场景的终端 WS 保活但**暂停 xterm 重绘**——
  // onmessage 把输出累积进 pendingRef 而不 write，切回激活时一次性 flush。无 Provider 时恒激活（FR-166/167 不变）。
  const { active: directorActive } = useDirectorRender()
  const pausedRef = useRef(!directorActive)
  const pendingRef = useRef<string[]>([])
  // 累积上限：长时间不激活的场景不无限攒内存（约对应 xterm scrollback），超限丢弃最旧段。
  const PENDING_MAX = 4000

  // 激活态切换：进入激活则一次性 flush 累积输出并 fit（瞬切回零延迟看到最新日志）；离开则转暂停。
  useEffect(() => {
    pausedRef.current = !directorActive
    if (directorActive && pendingRef.current.length > 0) {
      const term = termRef.current
      if (term) {
        for (const chunk of pendingRef.current) term.write(chunk)
        pendingRef.current = []
      }
    }
  }, [directorActive])

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
  const [drawerOpen, setDrawerOpen] = useState(false)

  // 把一行命令下发并入历史
  const submitLine = useCallback(() => {
    const ws = wsRef.current
    const line = lineBufRef.current
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'stdin', instanceId, data: line }))
    }
    termRef.current?.write('\r\n')
    if (line.trim()) {
      historyRef.current = [...historyRef.current, line].slice(-200)
      setHistory(historyRef.current)
    }
    histIdxRef.current = -1
    lineBufRef.current = ''
  }, [instanceId])

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

  const connect = useCallback(() => {
    if (!wsUrl || !token) return
    cleanupRef.current = false

    const ws = new WebSocket(`${wsUrl}?token=${token}`)
    wsRef.current = ws

    ws.onopen = () => { retryCountRef.current = 0 }

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data)
        if (msg.type === 'stdout' || msg.type === 'stderr') {
          const text = String(msg.data ?? '')
          const out = text.replace(/\r?\n/g, '\r\n')
          // 非激活：累积不重绘（WS 仍在收，瞬切回再 flush，零延迟且不丢数据）。
          if (pausedRef.current) {
            pendingRef.current.push(out)
            if (pendingRef.current.length > PENDING_MAX) pendingRef.current.shift()
          } else {
            termRef.current?.write(out)
          }
          // 解析在线玩家：逐完整行匹配加入/离开/list 输出
          parseBufRef.current += text
          let nl: number
          while ((nl = parseBufRef.current.indexOf('\n')) >= 0) {
            const raw = parseBufRef.current.slice(0, nl)
            parseBufRef.current = parseBufRef.current.slice(nl + 1)
            // 去 ANSI 颜色码与 CR：Paper 控制台给玩家名套色，否则玩家名被转义码包裹导致正则匹配不到
            // eslint-disable-next-line no-control-regex -- ANSI 转义符为有意匹配
            const line = raw.replace(/\x1b\[[0-9;]*[A-Za-z]/g, '').replace(/\r/g, '')
            const join = line.match(/([A-Za-z0-9_]{1,16}) joined the game/)
            if (join) onlinePlayersRef.current.add(join[1])
            const left = line.match(/([A-Za-z0-9_]{1,16}) left the game/)
            if (left) onlinePlayersRef.current.delete(left[1])
            const list = line.match(/players online:\s*(.+)$/)
            if (list) onlinePlayersRef.current = new Set(list[1].split(/,\s*/).map((s) => s.trim()).filter(Boolean))
          }
        } else if (msg.type === 'state') {
          const line = `\r\n[状态: ${msg.state}]\r\n`
          if (pausedRef.current) {
            pendingRef.current.push(line)
            if (pendingRef.current.length > PENDING_MAX) pendingRef.current.shift()
          } else {
            termRef.current?.write(line)
          }
        }
      } catch {
        termRef.current?.write(event.data)
      }
    }

    ws.onclose = () => { if (!cleanupRef.current) termRef.current?.write('\r\n[连接已断开]\r\n') }

    ws.onerror = () => {
      if (cleanupRef.current) return
      if (retryCountRef.current < MAX_RETRIES) {
        retryCountRef.current++
        // eslint-disable-next-line react-hooks/immutability -- 重试定时器经 ref 记录，connect 递归重连为既定模式
        retryTimerRef.current = setTimeout(() => { if (!cleanupRef.current) connect() }, BASE_RETRY_DELAY * retryCountRef.current)
      } else {
        termRef.current?.write('\r\n[连接错误]\r\n')
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- connect 递归重连，仅依赖连接参数
  }, [wsUrl, token, instanceId])

  useEffect(() => {
    if (!terminalRef.current) return

    const term = new Terminal({
      cursorBlink: true,
      disableStdin: false,
      fontSize: 14,
      fontFamily: 'Consolas, Monaco, monospace',
      theme: { background: '#1a1b26', foreground: '#a9b1d6', cursor: '#c0caf5' },
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(terminalRef.current)
    fitAddon.fit()
    termRef.current = term

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

    term.onData((data) => {
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

    const handleResize = () => {
      fitAddon.fit()
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.send(JSON.stringify({ type: 'resize', instanceId, cols: term.cols, rows: term.rows }))
      }
    }
    window.addEventListener('resize', handleResize)

    if (!isLoading && wsUrl && token) connect()

    return () => {
      window.removeEventListener('resize', handleResize)
      cleanupRef.current = true
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      wsRef.current?.close()
      term.dispose()
    }
    // 故意不依赖 readOnly：实例状态变化不重建终端/不断连。
  }, [instanceId, connect, isLoading, wsUrl, token, submitLine, replaceLine, copySelection])

  if (isLoading) {
    return (
      <div className="w-full h-full min-h-[400px] bg-[#1a1b26] rounded-md flex items-center justify-center">
        <div className="flex items-center gap-2 text-gray-400 text-sm">
          <div className="h-4 w-4 animate-spin rounded-full border-2 border-gray-400 border-t-transparent" />
          连接中…
        </div>
      </div>
    )
  }

  return (
    <div className="relative flex h-full min-h-[400px] w-full gap-0">
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
        title="历史命令"
      >
        {drawerOpen ? '▶' : '◀'} 历史
      </button>

      {/* 历史命令抽屉 */}
      {drawerOpen && (
        <div className="flex w-56 flex-col rounded-md border-l border-white/10 bg-[#16161e]">
          <div className="border-b border-white/10 px-3 py-2 text-xs font-medium text-gray-300">历史命令（点重发）</div>
          <div className="min-h-0 flex-1 overflow-y-auto p-1">
            {history.length === 0 ? (
              <div className="p-2 text-xs text-gray-500">暂无</div>
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

      {/* 右键菜单 */}
      {menu && (
        <>
          <div className="fixed inset-0 z-20" onClick={() => setMenu(null)} onContextMenu={(e) => { e.preventDefault(); setMenu(null) }} />
          <div
            className="fixed z-30 min-w-36 rounded-md border border-white/10 bg-[#1f2030] py-1 text-sm text-gray-200 shadow-lg"
            style={{ left: menu.x, top: menu.y }}
          >
            <button type="button" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { copySelection(); setMenu(null) }}>复制选中</button>
            <button type="button" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { selectAll(); setMenu(null) }}>全选</button>
            <button type="button" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { copyAll(); setMenu(null) }}>复制全部</button>
            <button type="button" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { saveLog(); setMenu(null) }}>保存日志</button>
            <div className="my-1 border-t border-white/10" />
            <button type="button" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { pasteClipboard(); setMenu(null) }}>粘贴</button>
            <button type="button" className="block w-full px-3 py-1 text-left hover:bg-white/10" onClick={() => { termRef.current?.clear(); setMenu(null) }}>清屏</button>
          </div>
        </>
      )}
    </div>
  )
}
