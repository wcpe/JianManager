import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 单条日志（实例运行日志或平台运行日志，FR-049）。 */
export interface LogEntry {
  id: number
  /** 来源：instance（实例）/ control_plane（平台）/ worker。 */
  source: string
  /** 级别：debug / info / warn / error。 */
  level: string
  instanceId: number
  instanceUuid: string
  nodeId: number
  /** 原始流名（stdout/stderr），仅实例日志。 */
  stream?: string
  message: string
  /** 日志产生时间（RFC3339）。 */
  time: string
}

/** 日志查询筛选条件（DB 侧过滤 + 分页，FR-049/FR-050）。 */
export interface LogQueryParams {
  /** 主视图：平台仅 CP；节点/实例聚合 Worker 与实例；all 仅平台管理员。 */
  view?: 'platform' | 'node_instance' | 'all' | 'legacy'
  source?: string
  level?: string
  instanceId?: number
  nodeId?: number
  /** 关键字，匹配 message。 */
  keyword?: string
  /** 起始时间（RFC3339）。 */
  from?: string
  /** 结束时间（RFC3339）。 */
  to?: string
  page?: number
  pageSize?: number
}

/** 日志分页响应。 */
export interface LogPage {
  items: LogEntry[]
  total: number
  page: number
  pageSize: number
}

/**
 * 游标分页查询参数（FR-419，spec §4.2）。
 *
 * 与页码分页并存：日志中心翻页仍用 {@link LogQueryParams} 的 `page/pageSize`（需要 total、
 * 需要跳页），控制台向上回溯用游标——回溯期间新日志持续涌入表头，OFFSET 的「跳过前 N 行」
 * 在两次请求间指向不同的行，会漂移出重复行或丢行。
 */
export interface LogCursorParams extends Omit<LogQueryParams, 'page' | 'pageSize'> {
  /** 上一页返回的 `nextCursor`；省略即从最新一条开始。 */
  cursor?: string
  /** 单页行数，上限 500（与 pageSize 同源）。传它即让后端切到游标模式。 */
  limit?: number
}

/** 游标分页响应（FR-419）。刻意无 total——游标模式不做全表 COUNT。 */
export interface LogCursorPage {
  items: LogEntry[]
  /** 下一页（更早）起点；`null` 表示已到最早，没有更早日志。 */
  nextCursor: string | null
  limit: number
}

/**
 * 取一页更早的日志（游标分页，FR-419）。
 *
 * 不走 useQuery：回溯是「按用户滚动逐页累积」的命令式流程，缓存键会随 cursor 变化而无限增长，
 * 且页与页之间必须严格串行（上一页的 nextCursor 是下一页的入参），交给 react-query 反而更绕。
 */
export async function fetchLogCursorPage(params: LogCursorParams): Promise<LogCursorPage> {
  const { data } = await api.get<LogCursorPage>('/logs', { params })
  return data
}

/** useLogs 可选项。 */
export interface UseLogsOptions {
  /**
   * 自动重拉间隔（ms）；用于「实时跟随」(tail, FR-150)。
   * 传 false 或省略则不轮询，仅在 params 变化时取数。
   */
  refetchInterval?: number | false
  enabled?: boolean
}

/**
 * 分页查询日志。
 * 过滤与分页全部在后端 DB 完成，不全量序列化（FR-049）。
 * keepPreviousData 让翻页/改筛选时旧页保留，避免表格闪烁。
 * 传 refetchInterval 时按间隔自动重拉，实现实时跟随（FR-150）。
 */
export function useLogs(params: LogQueryParams, options: UseLogsOptions = {}) {
  return useQuery({
    queryKey: ['logs', params],
    queryFn: async () => {
      const { data } = await api.get<LogPage>('/logs', { params })
      return data
    },
    placeholderData: (prev) => prev,
    refetchInterval: options.refetchInterval ?? false,
    enabled: options.enabled ?? true,
  })
}

export interface LegacyLogPage extends LogPage {
  sourceTag: 'legacy'
  coverage?: Record<string, unknown>
}

export function useLegacyLogs(params: LogQueryParams, enabled: boolean) {
  // legacy 路径不接受 view 参数（视图维度由 federated 侧承担），构造参数时剔除。
  const legacyParams = { ...params }
  delete legacyParams.view
  return useQuery({
    queryKey: ['logsLegacy', legacyParams],
    queryFn: async () => (await api.get<LegacyLogPage>('/logs/legacy', { params: { ...legacyParams, source: 'legacy' } })).data,
    enabled,
    placeholderData: (prev) => prev,
  })
}

/**
 * 按当前筛选导出日志为 NDJSON 文件并触发浏览器下载（GET /logs/export）。
 * 经 api 客户端发起，自动携带鉴权并复用 401 刷新；分页参数不参与导出。
 */
export async function exportLogs(params: LogQueryParams): Promise<void> {
  const exportParams: LogQueryParams = { ...params }
  delete exportParams.page
  delete exportParams.pageSize

  const { data } = await api.get('/logs/export', {
    params: exportParams,
    responseType: 'blob',
  })
  const url = URL.createObjectURL(data as Blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `logs-${new Date().toISOString().replace(/[:.]/g, '-')}.ndjson`
  a.click()
  URL.revokeObjectURL(url)
}
