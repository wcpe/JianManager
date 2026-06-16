import { useEffect, useRef, useCallback } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

interface TerminalComponentProps {
  instanceId: string
  wsUrl?: string
  token?: string
  readOnly?: boolean
}

export default function TerminalComponent({ instanceId, wsUrl, token, readOnly = false }: TerminalComponentProps) {
  const terminalRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const cleanupRef = useRef(false)

  const connect = useCallback(() => {
    if (!wsUrl || !token) return
    cleanupRef.current = false

    const ws = new WebSocket(`${wsUrl}?token=${token}`)
    wsRef.current = ws

    ws.onopen = () => {
      console.log('Terminal connected', instanceId)
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
      if (!cleanupRef.current) {
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

    // 连接 WebSocket
    connect()

    return () => {
      window.removeEventListener('resize', handleResize)
      cleanupRef.current = true
      wsRef.current?.close()
      term.dispose()
    }
  }, [instanceId, readOnly, connect])

  return (
    <div
      ref={terminalRef}
      className="w-full h-full min-h-[400px] bg-[#1a1b26] rounded-md overflow-hidden"
    />
  )
}
