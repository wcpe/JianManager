// 说明（2026-10-29）：本文件原本还导出 useLogs / useLegacyLogs / fetchLogCursorPage /
// exportLogs / exportCurrentLogPage —— 它们都直接请求 CP 的 `/logs`、`/logs/export`、
// `/logs/legacy`，而这三个端点在 CP `logs` 表删除后已不存在，故一并删除。
//
// 保留的是**类型与纯工具**：LogEntry / LogQueryParams 等仍被日志中心、控制台历史、
// 联邦查询与实例控制台使用；downloadLogBlob / sameLogItems 是联邦导出复用的纯函数，
// 与“日志从哪来”无关。日志数据现在只有两个来源：VL 联邦（实例）与进程日志文件（平台）。

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
  /**
   * 主视图：平台仅 CP；节点/实例聚合 Worker 与实例；all 仅平台管理员。
   *
   * 曾有 `legacy`（CP `logs` 表的切换前存量），随该表删除而下线（2026-10-29）。
   */
  view?: 'platform' | 'node_instance' | 'all'
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
/** 仅在请求仍属于当前页面时下载，卸载或用户取消后不得偷偷交付附件。 */
export function downloadLogBlob(blob: Blob, prefix: string, signal?: AbortSignal): void {
  signal?.throwIfAborted()
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `${prefix}-${new Date().toISOString().replace(/[:.]/g, '-')}.ndjson`
  anchor.click()
  // 等浏览器消费点击后再回收，避免真实浏览器中附件偶发为空。
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

/** 对比完整原始行而非虚拟挂载行或展示映射；键顺序不影响内容身份。 */
export function sameLogItems(left: readonly unknown[], right: readonly unknown[]): boolean {
  const canonical = (value: unknown): string => {
    if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`
    if (value && typeof value === 'object') {
      return `{${Object.entries(value).filter(([, v]) => v !== undefined).sort(([a], [b]) => a.localeCompare(b))
        .map(([key, v]) => `${JSON.stringify(key)}:${canonical(v)}`).join(',')}}`
    }
    return JSON.stringify(value) ?? 'null'
  }
  return canonical(left) === canonical(right)
}
