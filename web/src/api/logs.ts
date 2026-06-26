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

/** useLogs 可选项。 */
export interface UseLogsOptions {
  /**
   * 自动重拉间隔（ms）；用于「实时跟随」(tail, FR-150)。
   * 传 false 或省略则不轮询，仅在 params 变化时取数。
   */
  refetchInterval?: number | false
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
