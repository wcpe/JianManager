import api from '@/api/client'

export type ClientDistExportKind = 'stats-summary' | 'dist-events' | 'security-logs'

export interface ClientDistExportFilters {
  channelId?: string
  range?: string
  errCode?: string
  outcome?: string
  eventKind?: 'manifest' | 'artifact'
  ip?: string
  machineId?: string
  playerName?: string
  version?: number
  type?: string
  from?: string
  to?: string
}

/** 请求后端生成 CSV，并返回服务端文件名与 Blob。 */
export async function exportClientDistCSV(kind: ClientDistExportKind, filters: ClientDistExportFilters) {
  const response = await api.get<Blob>('/client-dist/export', {
    params: compactExportParams({ kind, ...filters }),
    responseType: 'blob',
  })
  return {
    blob: response.data,
    filename: parseExportFilename(response.headers['content-disposition']) ?? `client-dist-${kind}.csv`,
  }
}

/** 触发浏览器保存 CSV。 */
export function saveClientDistCSV(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = filename
  anchor.click()
  URL.revokeObjectURL(url)
}

function compactExportParams(input: Record<string, unknown>) {
  return Object.fromEntries(Object.entries(input).filter(([, value]) => value !== undefined && value !== null && value !== ''))
}

function parseExportFilename(disposition?: string): string | null {
  const match = disposition?.match(/filename="?([^";]+)"?/i)
  return match?.[1] ?? null
}
