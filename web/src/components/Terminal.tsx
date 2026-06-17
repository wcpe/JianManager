import { useEffect, useRef, useCallback } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

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

export default function TerminalComponent({ instanceId, wsUrl, token, readOnly = false, isLoading = false }: TerminalComponentProps) {
  const terminalRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const cleanupRef = useRef(false)
  const retryCountRef = useRef(0)
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const connect = useCallback(() => {
    if (!wsUrl || !token) return
    cleanupRef.current = false

    const ws = new WebSocket(`${wsUrl}?token=${token}`)
    wsRef.current = ws

    ws.onopen = () => {
      retryCountRef.current = 0
    }

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data)
        if (msg.type === 'stdout' || msg.type === 'stderr') {
          termRef.current?.write(msg.data)
        } else if (msg.type === 'state') {
          termRef.current?.write(`\r\n[状态: ${msg.state}]\r\n`)
        }
      } catch {
        termRef.current?.write(event.data)
      }
    }

    ws.onclose = () => {
      if (!cleanupRef.current) {
        termRef.current?.write('\r\n[连接已断开]\r\n')
      }
    }

    ws.onerror = () => {
      if (cleanupRef.current) return

      // 重试逻辑：首次失败静默重试，全部失败后才显示错误
      if (retryCountRef.current < MAX_RETRIES) {
        retryCountRef.current++
        const delay = BASE_RETRY_DELAY * retryCountRef.current
        retryTimerRef.current = setTimeout(() => {
          if (!cleanupRef.current) {
            connect()
          }
        }, delay)
      } else {
        termRef.current?.write('\r\n[连接错误]\r\n')
      }
    }
  }, [wsUrl, token, instanceId])

  useEffect(() => {
    if (!terminalRef.current) return

    const term = new Terminal({
      cursorBlink: !readOnly,
      disableStdin: readOnly,
      fontSize: 14,
      fontFamily: 'Consolas, Monaco, monospace',
      theme: {
        background: '#1a1b26',
        foreground: '#a9b1d6',
        cursor: '#c0caf5',
      },
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(terminalRef.current)
    fitAddon.fit()
    termRef.current = term

    // 用户输入 → stdin
    if (!readOnly) {
      term.onData((data) => {
        if (wsRef.current?.readyState === WebSocket.OPEN) {
          wsRef.current.send(JSON.stringify({
            type: 'stdin',
            instanceId,
            data,
          }))
        }
      })
    }

    // 窗口大小变化
    const handleResize = () => {
      fitAddon.fit()
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.send(JSON.stringify({
          type: 'resize',
          instanceId,
          cols: term.cols,
          rows: term.rows,
        }))
      }
    }
    window.addEventListener('resize', handleResize)

    // token 已就绪时连接 WebSocket
    if (!isLoading && wsUrl && token) {
      connect()
    }

    return () => {
      window.removeEventListener('resize', handleResize)
      cleanupRef.current = true
      if (retryTimerRef.current) {
        clearTimeout(retryTimerRef.current)
      }
      wsRef.current?.close()
      term.dispose()
    }
  }, [instanceId, readOnly, connect, isLoading, wsUrl, token])

  // token 加载中：显示占位
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
    <div
      ref={terminalRef}
      className="w-full h-full min-h-[400px] bg-[#1a1b26] rounded-md overflow-hidden"
    />
  )
}
