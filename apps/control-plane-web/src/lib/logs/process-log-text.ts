import type { LogEntry } from '@/api/logs'

/** File windows are snapshots, not searchable history. Keep unknown timestamps unknown. */
export function processLogEntries(content: string, source: 'worker' | 'control_plane', nodeId = 0): LogEntry[] {
  return content.split('\n').filter(Boolean).map((message, index) => {
    let time = /\btime=(\S+)/.exec(message)?.[1] ?? ''
    let level = /\blevel=["']?([A-Za-z]+)/.exec(message)?.[1] ?? ''
    try {
      const record: unknown = JSON.parse(message)
      if (record && typeof record === 'object') {
        const row = record as Record<string, unknown>
        if (typeof row.time === 'string') time = row.time
        if (typeof row.level === 'string') level = row.level
      }
    } catch { /* Plain text and multiline continuations are valid log content. */ }
    level = level.toLowerCase()
    if (level === 'warning') level = 'warn'
    if (!['debug', 'info', 'warn', 'error', 'fatal', 'trace'].includes(level)) level = 'unknown'
    return { id: index, source, level, message, time, nodeId, instanceId: 0, instanceUuid: '' }
  }).reverse()
}

export function filterProcessLogs(items: LogEntry[], keyword: string, level: string): LogEntry[] {
  const needle = keyword.trim().toLocaleLowerCase()
  return items.filter((item) => (!level || item.level === level)
    && (!needle || item.message.toLocaleLowerCase().includes(needle)))
}
