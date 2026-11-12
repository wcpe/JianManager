import type { LogEntry } from '@/lib/console/console-log-types'

export interface LogSnapshotWindow {
  key: string
  label: string
  source: 'control_plane' | 'worker' | 'instance'
  status: 'ready' | 'loading' | 'failed' | 'offline' | 'skipped'
  loadedRows: number
  limit: number
  capturedAt?: string
  from?: string
  to?: string
  truncated?: boolean
  note?: string
}

type WindowMetadata = {
  type: 'window_metadata'
  scope: 'loaded_window_only'
  complete_history: false
  exportedAt: string
  windows: readonly LogSnapshotWindow[]
}

type ExportLogRow = {
  type: 'log'
  source: string
  level: string
  nodeId: number
  instanceId: number
  sourceRef?: string
  stream?: string
  time: string
  message: string
  fields?: Record<string, unknown>
}

/**
 * Serialize exactly the rows currently visible in the workbench.
 * The metadata deliberately describes a bounded snapshot, never a history export.
 */
export function buildLogSnapshot(
  items: readonly LogEntry[],
  windows: readonly LogSnapshotWindow[],
  exportedAt = new Date().toISOString(),
): string {
  const metadata: WindowMetadata = {
    type: 'window_metadata',
    scope: 'loaded_window_only',
    complete_history: false,
    exportedAt,
    windows,
  }
  const rows = items.map<ExportLogRow>((item) => ({
    type: 'log',
    source: item.source,
    level: item.level,
    nodeId: item.nodeId,
    instanceId: item.instanceId,
    ...(item.sourceRef ? { sourceRef: item.sourceRef } : {}),
    ...(item.stream ? { stream: item.stream } : {}),
    // Keep unknown timestamps as the original empty value. Never invent `now`.
    time: item.time,
    message: item.message,
    ...(item.fields === undefined ? {} : { fields: item.fields }),
  }))
  return [JSON.stringify(metadata), ...rows.map((row) => JSON.stringify(row))].join('\n') + '\n'
}

/** Download a non-empty current snapshot as NDJSON and release its object URL. */
export function downloadLogSnapshot(
  items: readonly LogEntry[],
  windows: readonly LogSnapshotWindow[],
  filenamePrefix = 'logs-snapshot',
): void {
  if (items.length === 0 || typeof document === 'undefined' || typeof URL === 'undefined') return
  const blob = new Blob([buildLogSnapshot(items, windows)], { type: 'application/x-ndjson;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  const stamp = new Date().toISOString().replace(/[:.]/g, '-')
  anchor.href = url
  anchor.download = `${filenamePrefix}-${stamp}.ndjson`
  anchor.click()
  URL.revokeObjectURL(url)
}
