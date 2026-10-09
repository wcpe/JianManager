/**
 * @file AgentCallLogsPageView：Agent 调用流水页的受控视图，取数与跳转由应用容器负责。
 * @input Panel/Button/Input/Select/Table/Badge 原语、layout 页壳（PageShell/PageHeader）、lucide-react 图标、翻译上下文
 * @output AgentCallLogsPageView、AgentCallLogsPageViewProps、AgentCallLogsQuery、AgentCallLogRow、AgentTokenOption
 * @sync apps/control-plane-web/src/pages/AgentCallLogsPage.tsx、apps/control-plane-web/src/pages/AgentCallLogsPage.dom.test.tsx
 * @since FR-502（组件受控化迁包；原页 FR-391 / FR-390 Agent 调用流水）
 */
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ScrollText } from 'lucide-react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'

/**
 * Radix Select 不接受空字符串值，用哨兵代表「该维度不过滤」。
 * 哨兵只是本视图的 Select 细节：对外契约里「不过滤」一律用空串表达。
 */
const SENTINEL_ALL = '__all__'

/**
 * 客户端枚举：与后端记录的 client 标识一一对应，是**闭集**而非数据列举，
 * 故下拉直接列全（不同于 Token 下拉，后者候选来自服务端返回的 Token 列表）。
 */
const CLIENT_OPTIONS = ['mcp', 'jmagent', 'curl', 'unknown'] as const

/**
 * 筛选与分页状态（受控）。
 *
 * 全部维度都会进入查询键并触发重新取数，故 state 由容器持有、逐字段注入：
 * 视图不自行缓存筛选草稿（保持原页「输入即生效」的语义），只负责把改动经 `onQueryChange` 上报。
 */
export interface AgentCallLogsQuery {
  /** Agent Token 主键的字符串形式；空串表示全部 Token。 */
  tokenId: string
  /** 操作名关键字（子串匹配）；空串表示不过滤。 */
  action: string
  /** 客户端标识；空串表示全部客户端。 */
  client: string
  /** 调用结果：`''` 全部 / `'true'` 成功 / `'false'` 失败（与后端布尔查询参数同形）。 */
  success: string
  /** 页码，从 1 起。 */
  page: number
}

/**
 * 流水行（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/agentObservability` 的 `AgentCallLogInfo`：
 * 容器直接传 API 返回的完整对象也结构兼容，无需把该 API 类型迁进包
 * （同 `McpActivityRow`、`BackupStorageRow` 的取舍）。
 */
export interface AgentCallLogRow {
  /** 行主键。 */
  id: number
  /** Token 主键：Token 无名称时兜底成 `#<id>`。 */
  tokenId: number
  /** Token 名称；可为空，空时回退为 `#<id>`。 */
  tokenName: string
  /** 操作名（同时用作 `audit.actions.*` 的中文翻译键）。 */
  action: string
  /** 客户端标识（`agentCallLogs.clients.*` 翻译键）。 */
  client: string
  /** 传输方式（可选；`agentCallLogs.transports.*` 翻译键），空显示 `—`。 */
  transport?: string
  /** 操作目标类型（可选，随目标 ID 一起附在操作名下方）。 */
  targetType?: string
  /** 操作目标 ID（可选）。 */
  targetId?: string
  /** 是否成功（失败时展示徽章 + 错误摘要）。 */
  success: boolean
  /** 失败原因（可选，仅失败行展示）。 */
  error?: string
  /** 处理耗时（毫秒）；缺失显示 `—`。 */
  latencyMs?: number
  /** 来源 IP；空显示 `—`。 */
  ip?: string
  /** 发生时间（RFC3339）。 */
  createdAt: string
}

/**
 * Token 下拉候选（容器取数注入）。
 * 只含非明文元数据：`tokenPrefix` 是后端给出的**前缀**，后端从不返回明文，视图不得展示或补全完整密钥。
 */
export interface AgentTokenOption {
  id: number
  name: string
  /** Token 前缀（非明文，仅用于人工对号）。 */
  tokenPrefix: string
}

/**
 * 受控边界：筛选、分页、取数三态与数据全部经 props 注入（改筛选要回到第 1 页这类
 * 「重新取数策略」由容器决定）；视图只保留展示与逐字段的改动上报。
 * 空/加载/错误三态的判定优先级、表格列、操作名与客户端的中文映射留在视图。
 */
export interface AgentCallLogsPageViewProps {
  /** 当前筛选与分页（受控）。 */
  query: AgentCallLogsQuery
  /**
   * 筛选维度变更：仅上报被改动的字段。
   * 页码复位（改筛选即回第 1 页）属取数策略，由容器负责，视图不代为重置。
   */
  onQueryChange: (patch: Partial<AgentCallLogsQuery>) => void
  /** 清除全部筛选条件并回到第 1 页。 */
  onClear: () => void
  /** Token 下拉候选（容器取数注入）；缺省即只有「全部 Token」一项。 */
  tokens?: AgentTokenOption[]
  /** 当前页流水行（空数组即空态）。 */
  items: AgentCallLogRow[]
  /** 命中总数（分页器据此算总页数）。 */
  total: number
  /** 每页条数（后端回显值，用于算总页数）。 */
  pageSize: number
  /** 取数中：展示加载文案。 */
  isLoading: boolean
  /** 取数失败：展示失败文案（优先于空态）。 */
  isError: boolean
  /** 后台取数中：汇总行追加「加载中」后缀，不清空已有内容。 */
  isFetching: boolean
  /**
   * 页头右端的路由跳转入口（容器注入 react-router `Link` 包裹的按钮；
   * 包内不依赖 react-router）。可省略——组件博物馆等无路由场景即不渲染跳转。
   */
  headerLinks?: ReactNode
}

/** Agent 调用流水页展示层（FR-391 / FR-390）：筛选条 + 流水表 + 简单翻页。 */
export function AgentCallLogsPageView({
  query,
  onQueryChange,
  onClear,
  tokens,
  items,
  total,
  pageSize,
  isLoading,
  isError,
  isFetching,
  headerLinks,
}: AgentCallLogsPageViewProps) {
  const { t } = useTranslation()
  const { tokenId, action, client, success, page } = query
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  return (
    // 全量对齐：外壳与页头沿用布局层原语。原先无 data-page，迁移时补上。
    // 标题内的图标带 aria-hidden，不影响 h1 的可访问名。
    <PageShell data-page="agent-call-logs">
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <ScrollText className="size-5 text-primary" aria-hidden />
            {t('agentCallLogs.title')}
          </span>
        }
        description={t('agentCallLogs.subtitle')}
        actions={headerLinks}
      />

      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={tokenId === '' ? SENTINEL_ALL : tokenId}
          onValueChange={(v: string) => {
            onQueryChange({ tokenId: v === SENTINEL_ALL ? '' : v })
          }}
        >
          <SelectTrigger size="sm" className="w-48">
            <SelectValue placeholder={t('agentCallLogs.allTokens')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('agentCallLogs.allTokens')}</SelectItem>
            {tokens?.map((tok) => (
              <SelectItem key={tok.id} value={String(tok.id)}>
                {tok.name} ({tok.tokenPrefix}…)
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Input
          value={action}
          onChange={(e) => {
            onQueryChange({ action: e.target.value })
          }}
          placeholder={t('agentCallLogs.actionPlaceholder')}
          className="h-9 w-48"
        />
        <Select
          value={client === '' ? SENTINEL_ALL : client}
          onValueChange={(v: string) => {
            onQueryChange({ client: v === SENTINEL_ALL ? '' : v })
          }}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue placeholder={t('agentCallLogs.allClients')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('agentCallLogs.allClients')}</SelectItem>
            {CLIENT_OPTIONS.map((c) => (
              <SelectItem key={c} value={c}>
                {t(`agentCallLogs.clients.${c}`, { defaultValue: c })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={success === '' ? SENTINEL_ALL : success}
          onValueChange={(v: string) => {
            onQueryChange({ success: v === SENTINEL_ALL ? '' : v })
          }}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue placeholder={t('agentCallLogs.allResults')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('agentCallLogs.allResults')}</SelectItem>
            <SelectItem value="true">{t('agentCallLogs.success')}</SelectItem>
            <SelectItem value="false">{t('agentCallLogs.failed')}</SelectItem>
          </SelectContent>
        </Select>
        <Button variant="outline" size="sm" onClick={onClear}>
          {t('agentCallLogs.clear')}
        </Button>
      </div>

      <p className="text-xs text-muted-foreground">
        {t('agentCallLogs.summary', { loaded: items.length, total })}
        {isFetching ? ` · ${t('common.loading')}` : ''}
      </p>

      {isLoading && <p className="text-sm text-muted-foreground">{t('common.loading')}</p>}
      {isError && <p className="text-sm text-destructive">{t('agentCallLogs.loadFailed')}</p>}

      {!isLoading && !isError && items.length === 0 && (
        <Panel className="p-6 text-center text-sm text-muted-foreground">{t('agentCallLogs.empty')}</Panel>
      )}

      {items.length > 0 && (
        <Panel className="overflow-x-auto p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('agentCallLogs.col.time')}</TableHead>
                <TableHead>{t('agentCallLogs.col.token')}</TableHead>
                <TableHead>{t('agentCallLogs.col.action')}</TableHead>
                <TableHead>{t('agentCallLogs.col.client')}</TableHead>
                <TableHead>{t('agentCallLogs.col.transport')}</TableHead>
                <TableHead>{t('agentCallLogs.col.result')}</TableHead>
                <TableHead>{t('agentCallLogs.col.latency')}</TableHead>
                <TableHead>{t('agentCallLogs.col.ip')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((row) => (
                <TableRow key={row.id}>
                  <TableCell className="whitespace-nowrap text-xs tabular-nums">
                    {row.createdAt ? new Date(row.createdAt).toLocaleString() : '—'}
                  </TableCell>
                  <TableCell className="text-xs">
                    <div className="font-medium">{row.tokenName || `#${row.tokenId}`}</div>
                  </TableCell>
                  <TableCell>
                    {(() => {
                      const label = t(`audit.actions.${row.action}`, { defaultValue: row.action })
                      return label === row.action ? (
                        <code className="font-mono text-[11px]">{row.action}</code>
                      ) : (
                        <div className="min-w-0">
                          <div className="truncate text-xs">{label}</div>
                          <code className="font-mono text-[10px] text-muted-foreground">{row.action}</code>
                        </div>
                      )
                    })()}
                    {(row.targetType || row.targetId) && (
                      <div className="text-[10px] text-muted-foreground">
                        {row.targetType}
                        {row.targetId ? `#${row.targetId}` : ''}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="text-xs">
                    {t(`agentCallLogs.clients.${row.client}`, { defaultValue: row.client })}
                  </TableCell>
                  <TableCell className="text-xs">
                    {row.transport
                      ? t(`agentCallLogs.transports.${row.transport}`, { defaultValue: row.transport })
                      : '—'}
                  </TableCell>
                  <TableCell>
                    <Badge variant={row.success ? 'default' : 'destructive'} className="text-[10px]">
                      {row.success ? t('agentCallLogs.success') : t('agentCallLogs.failed')}
                    </Badge>
                    {!row.success && row.error && (
                      <div className="mt-0.5 max-w-[12rem] truncate text-[10px] text-muted-foreground" title={row.error}>
                        {row.error}
                      </div>
                    )}
                  </TableCell>
                  <TableCell className="tabular-nums text-xs">
                    {row.latencyMs != null ? `${row.latencyMs} ms` : '—'}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{row.ip || '—'}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}

      {totalPages > 1 && (
        <div className="flex items-center justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={page <= 1}
            onClick={() => onQueryChange({ page: Math.max(1, page - 1) })}
          >
            {t('agentCallLogs.prev')}
          </Button>
          <span className="text-xs text-muted-foreground">
            {page} / {totalPages}
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={page >= totalPages}
            onClick={() => onQueryChange({ page: page + 1 })}
          >
            {t('agentCallLogs.next')}
          </Button>
        </div>
      )}
    </PageShell>
  )
}
