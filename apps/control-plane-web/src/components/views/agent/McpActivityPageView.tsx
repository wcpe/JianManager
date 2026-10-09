/**
 * @file McpActivityPageView：MCP 活动页的受控视图，取数、刷新、端点复制提示与跳转由应用容器负责。
 * @input Panel/Button/Table 原语、layout 页壳（PageShell/PageHeader）、lucide-react 图标、翻译上下文
 * @output McpActivityPageView、McpActivityPageViewProps、McpActivityRow、WINDOW_PRESETS、McpWindowPreset
 * @sync apps/control-plane-web/src/pages/McpActivityPage.tsx、apps/control-plane-web/src/pages/McpActivityPage.dom.test.tsx、apps/control-plane-web/e2e/mcp-activity.spec.ts
 * @since FR-502（组件受控化迁包；原 FR-391 / ADR-096 去会话化后的 Token 级活动视图）
 */
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Cable, RefreshCw } from 'lucide-react'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { Button } from '@jianmanager/ui/components/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@jianmanager/ui/components/table'

/**
 * 统计窗口档位：取值会原样作为 `window` 查询参数发给后端，因此必须能被
 * `internal/controlplane/mcp/handler.go` 的 `ListActivity` 按 Go duration 解析，
 * 并落在闭区间 1h~168h（同文件的 minActivityWindow / maxActivityWindow），否则该请求被 400 拒绝。
 * Go duration 没有「天」单位，所以「7 天」只能写 `168h`——写成 `7d` 会被后端判为非法。
 * 档位与后端可解析性的契约由 McpActivityPage.dom.test.tsx 的形态/区间守卫测试兜底
 * （该测试经应用侧 `McpActivityPage.tsx` 再导出本常量，逐项枚举校验）。
 */
// eslint-disable-next-line react-refresh/only-export-components -- 档位是「后端可解析性」契约的一部分，需与渲染它的组件同文件导出并供单测逐项枚举（仅影响 Fast Refresh）
export const WINDOW_PRESETS = ['24h', '168h'] as const

/** 统计窗口档位取值（由 `WINDOW_PRESETS` 派生）。 */
export type McpWindowPreset = (typeof WINDOW_PRESETS)[number]

/**
 * 活动行（本视图渲染所需的最小字段集）。
 *
 * 刻意只声明用到的字段，而非照搬应用侧 `@/api/agentObservability` 的 `McpActivityItem`：
 * 容器直接传 API 返回的完整对象也结构兼容，无需把该 API 类型迁进包
 * （同 `BackupStorageRow`、`ArtifactVersionsPageView` 的取舍）。
 *
 * 脱敏边界：`tokenPrefix` 是后端给出的**前缀**，后端从不返回明文 Token，
 * 视图任何位置都不得展示或补全完整密钥。
 */
export interface McpActivityRow {
  /** Token 主键：行 key，并在 Token 无名称时兜底成 `Token #<id>`。 */
  tokenId: number
  /** Token 名称；可为空，空时回退为 `Token #<id>`。 */
  tokenName: string
  /** Token 前缀（非明文，仅用于人工对号）。 */
  tokenPrefix: string
  /** 窗口内最近一次活动时间（RFC3339）；空表示窗口内无活动。 */
  lastActivityAt: string
  /** 窗口内最近一次操作名；空显示 `—`。 */
  lastAction: string
  /** 窗口内调用总数。 */
  callCount: number
  /** 窗口内失败调用数。 */
  failureCount: number
  /** 窗口内来源 IP 列表（空显示 `—`）。 */
  clientIPs: string[]
  /** 客户端标识 → 该窗口内调用次数（展示时按次数降序）。 */
  clients: Record<string, number>
}

/**
 * 受控边界：窗口档位是**受控**值（切档会改查询键并触发重新取数，故 state 由容器持有）；
 * 取数、刷新、端点基址计算、复制结果 toast 与页头跳转一律在容器。
 * 视图只保留展示：档位按钮的选中态、端点说明、表格与加载/错误/空三态的判定优先级。
 */
export interface McpActivityPageViewProps {
  /** 当前统计窗口档位（受控：容器据此发起请求）。 */
  window: McpWindowPreset
  /** 切换统计窗口档位。 */
  onWindowChange: (preset: McpWindowPreset) => void
  /** 窗口内按 Token 聚合的活动行（空数组即空态）。 */
  items: McpActivityRow[]
  /** 服务端聚合生成时间（RFC3339）；空则不展示该行。 */
  generatedAt?: string
  /** 取数中：展示加载文案（表格与空态不渲染）。 */
  isLoading: boolean
  /** 取数失败：展示失败文案（优先于空态）。 */
  isError: boolean
  /** 后台刷新中：仅驱动刷新按钮的禁用与图标旋转，不影响表格内容。 */
  isFetching: boolean
  /** 手动刷新（容器转 `refetch`）。 */
  onRefresh: () => void
  /** MCP 端点基址（同源推导留在容器，包内不读 `window.location`）。 */
  endpoint: string
  /** 复制端点：成功/失败提示的 toast 文案由容器决定。 */
  onCopyEndpoint: () => void
  /**
   * 页头右端的路由跳转入口（容器注入 react-router `Link` 包裹的按钮；
   * 包内不依赖 react-router）。可省略——组件博物馆等无路由场景即不渲染跳转。
   */
  headerLinks?: ReactNode
}

/** 客户端标识 → 调用次数 映射转成「客户端 ×次数」列表，按次数降序。 */
function formatClients(clients: Record<string, number> | undefined): string {
  const entries = Object.entries(clients ?? {})
  if (entries.length === 0) return '—'
  return entries
    .sort((a, b) => b[1] - a[1])
    .map(([client, count]) => `${client} ×${count}`)
    .join('，')
}

/**
 * MCP 活动运维页展示层（FR-391 / ADR-096）：按 Token 聚合的调用活动、MCP URL 展示与吊销入口。
 * 端点去会话化后已无「会话」可观测，故本视图只呈现窗口内的活动聚合。
 */
export function McpActivityPageView({
  window: windowValue,
  onWindowChange,
  items,
  generatedAt,
  isLoading,
  isError,
  isFetching,
  onRefresh,
  endpoint,
  onCopyEndpoint,
  headerLinks,
}: McpActivityPageViewProps) {
  const { t } = useTranslation()

  return (
    // 全量对齐：外壳与页头沿用布局层原语。原先无 data-page，迁移时补上。
    // 标题内的图标带 aria-hidden，不影响 h1 的可访问名。
    <PageShell data-page="mcp-activity">
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <Cable className="size-5 text-primary" aria-hidden />
            {t('mcpActivity.title')}
          </span>
        }
        description={t('mcpActivity.subtitle')}
        actions={
          <>
            <div
              className="flex items-center gap-1 rounded-md border p-0.5"
              role="group"
              aria-label={t('mcpActivity.window')}
            >
              {WINDOW_PRESETS.map((w) => (
                <Button
                  key={w}
                  variant={windowValue === w ? 'secondary' : 'ghost'}
                  size="sm"
                  aria-pressed={windowValue === w}
                  onClick={() => onWindowChange(w)}
                >
                  {t(`mcpActivity.window_${w}`)}
                </Button>
              ))}
            </div>
            <Button variant="outline" size="sm" onClick={onRefresh} disabled={isFetching}>
              <RefreshCw className={`size-3.5 ${isFetching ? 'animate-spin' : ''}`} />
              {t('mcpActivity.refresh')}
            </Button>
            <Button variant="outline" size="sm" onClick={onCopyEndpoint}>
              {t('mcpActivity.copyUrl')}
            </Button>
            {headerLinks}
          </>
        }
      />

      <Panel className="space-y-2 p-3 text-sm text-muted-foreground">
        <div>
          <span className="font-medium text-foreground">{t('mcpActivity.endpoint')}: </span>
          <code className="break-all font-mono text-xs">{endpoint}</code>
        </div>
        <p>{t('mcpActivity.endpointHint')}</p>
        <p className="text-xs">{t('mcpActivity.endpointHintSse')}</p>
        <p className="text-xs">{t('mcpActivity.authNote')}</p>
        <p className="text-xs">{t('mcpActivity.protocolVersion')}</p>
        {generatedAt && (
          <p className="text-xs">
            {t('mcpActivity.generatedAt', { time: new Date(generatedAt).toLocaleString() })}
          </p>
        )}
      </Panel>

      {isLoading && <p className="text-sm text-muted-foreground">{t('common.loading')}</p>}
      {isError && <p className="text-sm text-destructive">{t('mcpActivity.loadFailed')}</p>}

      {!isLoading && !isError && items.length === 0 && (
        <Panel className="p-6 text-center text-sm text-muted-foreground">{t('mcpActivity.empty')}</Panel>
      )}

      {items.length > 0 && (
        <Panel className="overflow-x-auto p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('mcpActivity.col.token')}</TableHead>
                <TableHead>{t('mcpActivity.col.client')}</TableHead>
                <TableHead>{t('mcpActivity.col.lastActivity')}</TableHead>
                <TableHead>{t('mcpActivity.col.lastAction')}</TableHead>
                <TableHead className="text-right">{t('mcpActivity.col.calls')}</TableHead>
                <TableHead className="text-right">{t('mcpActivity.col.failures')}</TableHead>
                <TableHead>{t('mcpActivity.col.ip')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {items.map((item) => (
                <TableRow key={item.tokenId}>
                  <TableCell>
                    <div className="font-medium">{item.tokenName || `Token #${item.tokenId}`}</div>
                    <code className="font-mono text-[11px] text-muted-foreground">
                      {item.tokenPrefix}…
                    </code>
                  </TableCell>
                  <TableCell
                    className="max-w-[12rem] truncate text-xs"
                    title={formatClients(item.clients)}
                  >
                    {formatClients(item.clients)}
                  </TableCell>
                  <TableCell className="whitespace-nowrap text-xs tabular-nums">
                    {item.lastActivityAt ? new Date(item.lastActivityAt).toLocaleString() : '—'}
                  </TableCell>
                  <TableCell
                    className="max-w-[10rem] truncate font-mono text-xs"
                    title={item.lastAction}
                  >
                    {item.lastAction || '—'}
                  </TableCell>
                  <TableCell className="text-right text-xs tabular-nums">{item.callCount}</TableCell>
                  <TableCell className="text-right text-xs tabular-nums">
                    {item.failureCount}
                  </TableCell>
                  <TableCell className="font-mono text-xs">
                    {item.clientIPs?.length ? item.clientIPs.join('，') : '—'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}
    </PageShell>
  )
}
