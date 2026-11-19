import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, GitCompareArrows, ListFilter, Table2 } from 'lucide-react'
import { Alert, AlertDescription } from '@jianmanager/ui/components/alert'
import { Button } from '@jianmanager/ui/components/button'
import { Input } from '@jianmanager/ui/components/input'
import { NativeSelect, NativeSelectOption } from '@jianmanager/ui/components/native-select'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { PaginationBar } from '@jianmanager/ui/components/pagination'
import { Panel } from '@jianmanager/ui/components/panel'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@jianmanager/ui/components/sheet'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { SummaryChips } from '@jianmanager/ui/components/summary-chips'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { EmptyState } from '@jianmanager/ui/components/empty-state'
import { useNodes } from '@/api/nodes'
import { useDebounced } from '@/lib/hooks/use-debounced'
import { formatRelativeTime } from '@/lib/shared/relative-time'
import {
  CONFIGURATION_ORDER,
  CONNECTION_ORDER,
  CONSISTENCY_ORDER,
  connectionLevel,
  consistencyLevel,
  formatCacheAge,
  formatDesiredVersion,
  formatObservedVersion,
  isDriftFilter,
  staleBridgeNote,
  useManagedMatrix,
  useManagedMatrixDetail,
  useManagedMatrixSummary,
  type ManagedMatrixRow,
} from '@/api/managedMatrix'
import { prettyJson } from '@/api/managedConfig'

/**
 * 探针托管配置对账矩阵（期望 vs 观测）。
 *
 * ## 三处必须由后端口径决定、前端不得自行重算的地方
 * 1. **`desiredVersion` 为 null 显示「未下发」，不是 0**。0 是探针首次托管前的合法版本号，
 *    把两者混在一列会让「从未下发」的机器看起来像「下发了初始版本」。
 * 2. **`cacheAgeSeconds` 为 null 显示「—」，不是 0**。0 表示「刚刚生效」，
 *    null 表示「从未成功应用过」——把 null 落成 0 就是把没托管过的机器说成刚同步完成。
 * 3. **页头汇总取 `/managed-matrix/summary`，不在前端按当前列表统计**。后端已把
 *    consistency 维从汇总里剔除，所以点进「漂移」后其它档位不会归零；前端若自己统计，
 *    必然随筛选一起归零，那会被误读成「问题都解决了」。
 *
 * ## 两维正交
 * 「配置来源（文件 / CP）」× 「连接状态（在线 / 降级 / 未授权 / 断开）」是两个独立轴，
 * 交叉表直接消费后端的 `crossMatrix`（同源的计数），前端只负责摆格子。
 */

/** 服务端分页每页条数。 */
const PAGE_SIZE = 20

/** 连接状态在交叉表里的列顺序固定，避免键序不同导致列跳动。 */
const CONNECTIONS = CONNECTION_ORDER

/** 配置来源在交叉表里的行顺序。 */
const CONFIGURATIONS = CONFIGURATION_ORDER

/** 从交叉矩阵里取一格计数（后端按「有数据的格子」返回，缺省即 0）。 */
function cellCount(cells: Array<{ configuration: string; connection: string; count: number }>, conf: string, conn: string): number {
  return cells.find((cell) => cell.configuration === conf && cell.connection === conn)?.count ?? 0
}

/** 单实例下钻面板：期望值、观测值、逐键来源与最近下发经历。 */
function InstanceDetailSheet({ instanceId, onClose }: { instanceId: number | null; onClose: () => void }) {
  const { t } = useTranslation()
  const detail = useManagedMatrixDetail(instanceId)
  const row = detail.data?.row
  const traced = detail.data?.traced ?? []

  return (
    <Sheet open={instanceId !== null} onOpenChange={(open) => { if (!open) onClose() }}>
      <SheetContent className="w-full overflow-auto sm:max-w-2xl">
        <SheetHeader>
          <SheetTitle>{row ? row.instanceName : t('managedMatrix.detail.title')}</SheetTitle>
          <SheetDescription>{t('managedMatrix.detail.subtitle')}</SheetDescription>
        </SheetHeader>

        {detail.isLoading ? (
          <p className="p-4 text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : detail.isError ? (
          <p className="p-4 text-sm text-destructive">{t('managedMatrix.detail.loadFailed')}</p>
        ) : !row ? null : (
          <div className="flex flex-col gap-3 p-4">
            <dl className="grid grid-cols-2 gap-2 text-xs">
              <div>
                <dt className="text-muted-foreground">{t('managedMatrix.colDesired')}</dt>
                <dd className="font-medium tabular-nums" data-testid="managed-detail-desired">
                  {formatDesiredVersion(row.desiredVersion, t('managedMatrix.notPushed'))}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('managedMatrix.colObserved')}</dt>
                <dd className="font-medium tabular-nums">
                  {formatObservedVersion(row.observedVersion, t('managedMatrix.neverReportedVersion'))}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('managedMatrix.colConfiguration')}</dt>
                <dd className="font-medium">{t(`managedMatrix.configuration.${row.configuration}`)}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('managedMatrix.colConnection')}</dt>
                <dd className="font-medium">{t(`managedMatrix.connection.${row.connection}`)}</dd>
              </div>
            </dl>

            {/* 逐键来源：回答「这个值是谁定的、哪一版定的」。 */}
            <div>
              <p className="mb-1 text-xs font-medium">{t('managedMatrix.detail.tracedTitle')}</p>
              {traced.length === 0 ? (
                <p className="text-xs text-muted-foreground">{t('managedMatrix.detail.tracedEmpty')}</p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t('managedMatrix.detail.colKey')}</TableHead>
                      <TableHead>{t('managedMatrix.detail.colValue')}</TableHead>
                      <TableHead>{t('managedMatrix.detail.colSource')}</TableHead>
                      <TableHead>{t('managedMatrix.detail.colTier')}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {traced.map((item) => (
                      <TableRow key={item.key} data-testid="managed-traced-row">
                        <TableCell className="font-mono text-xs">{item.key}</TableCell>
                        <TableCell className="max-w-[220px] truncate font-mono text-xs" title={prettyJson(item.value)}>
                          {item.deleted ? t('managedMatrix.detail.deleted') : prettyJson(item.value)}
                        </TableCell>
                        <TableCell className="text-xs">
                          {t(`managedConfig.scope.${item.scope}`)}
                          {item.scope === 'global' ? '' : ` / ${item.scopeName || `#${item.scopeRefId}`}`}
                          {` · v${item.version}`}
                        </TableCell>
                        <TableCell className="text-xs">
                          <StatusBadge
                            level={item.tierForbidden ? 'danger' : item.tier === 'guarded' ? 'warning' : 'neutral'}
                            label={item.tier}
                            dot={false}
                          />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </div>

            <div className="grid gap-2 md:grid-cols-2">
              <div>
                <p className="mb-1 text-xs font-medium">{t('managedMatrix.detail.desiredValues')}</p>
                <pre className="max-h-64 overflow-auto rounded-md bg-muted p-2 text-xs">
                  {prettyJson(detail.data?.desiredValues) || t('managedMatrix.detail.noValues')}
                </pre>
              </div>
              <div>
                <p className="mb-1 text-xs font-medium">{t('managedMatrix.detail.observedEffective')}</p>
                <pre className="max-h-64 overflow-auto rounded-md bg-muted p-2 text-xs">
                  {prettyJson(detail.data?.observedEffective) || t('managedMatrix.detail.noValues')}
                </pre>
              </div>
            </div>

            <div>
              <p className="mb-1 text-xs font-medium">{t('managedMatrix.detail.recentPushes')}</p>
              {(detail.data?.recentPushes ?? []).length === 0 ? (
                <p className="text-xs text-muted-foreground">{t('managedMatrix.detail.recentEmpty')}</p>
              ) : (
                <ul className="space-y-1 text-xs">
                  {(detail.data?.recentPushes ?? []).map((item) => (
                    <li key={item.id} className="flex flex-wrap items-center gap-2">
                      <span className="tabular-nums text-muted-foreground">#{item.sessionId}</span>
                      <StatusBadge level={consistencyLevel(item.state === 'applied' ? 'inSync' : 'failed')} label={item.state} dot={false} />
                      <span className="text-muted-foreground">
                        {t('managedMatrix.detail.pushVersions', { desired: item.desiredVersion, base: item.baseVersion })}
                      </span>
                      {item.error ? <span className="text-destructive">{item.error}</span> : null}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}

/** 对账矩阵页。 */
export default function ManagedMatrixPage() {
  const { t } = useTranslation()

  const [keyword, setKeyword] = useState('')
  const debouncedKeyword = useDebounced(keyword, 300)
  const [consistency, setConsistency] = useState('')
  const [connection, setConnection] = useState('')
  const [configuration, setConfiguration] = useState('')
  const [nodeId, setNodeId] = useState(0)
  const [page, setPage] = useState(1)
  const [detailId, setDetailId] = useState<number | null>(null)

  const nodesQuery = useNodes()
  const params = useMemo(
    () => ({
      ...(debouncedKeyword ? { keyword: debouncedKeyword } : {}),
      ...(consistency ? { consistency } : {}),
      ...(connection ? { connection } : {}),
      ...(configuration ? { configuration } : {}),
      ...(nodeId ? { nodeId } : {}),
      page,
      pageSize: PAGE_SIZE,
    }),
    [debouncedKeyword, consistency, connection, configuration, nodeId, page],
  )

  const matrixQuery = useManagedMatrix(params)
  // 汇总只受「节点 / 关键字」约束：把 consistency 等筛选传进去也无效（后端清空该维），
  // 这里显式只传会真正收窄范围的两项，让调用点就表达出「汇总不受档位筛选影响」。
  const summaryQuery = useManagedMatrixSummary({ ...(debouncedKeyword ? { keyword: debouncedKeyword } : {}), ...(nodeId ? { nodeId } : {}) })

  const rows = matrixQuery.data?.items ?? []
  const total = matrixQuery.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / (matrixQuery.data?.pageSize ?? PAGE_SIZE)))
  const summary = summaryQuery.data

  /** 切筛选时回到第一页：留着旧页码会看到「第 3 页空表」。 */
  const applyFilter = (updater: () => void) => {
    updater()
    setPage(1)
  }

  return (
    <PageShell>
      <PageHeader title={t('managedMatrix.title')} description={t('managedMatrix.description')} />

      {/* 页头汇总 + 两维正交：数据全部来自 /managed-matrix/summary，前端不做二次统计。 */}
      <Panel title={t('managedMatrix.summaryTitle')} icon={<ListFilter className="size-4" />}>
        <div className="flex flex-col gap-3">
          <SummaryChips
            chips={CONSISTENCY_ORDER.map((key) => ({
              key,
              label: t('managedMatrix.chipConsistency', {
                label: t(`managedMatrix.consistency.${key}`),
                count: summary?.byConsistency[key] ?? 0,
              }),
              level: consistencyLevel(key),
              active: consistency === key,
              onClick: () => applyFilter(() => setConsistency(consistency === key ? '' : key)),
            }))}
          />

          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              size="sm"
              variant={isDriftFilter(consistency) ? 'default' : 'outline'}
              onClick={() => applyFilter(() => setConsistency(isDriftFilter(consistency) ? '' : 'drift'))}
              data-testid="managed-matrix-only-drift"
            >
              <AlertTriangle />
              {t('managedMatrix.onlyDrift')}
            </Button>
            <span className="text-xs text-muted-foreground">
              {t('managedMatrix.summaryTotal', { total: summary?.total ?? 0 })}
            </span>
          </div>

          {/* 交叉矩阵：配置来源 × 连接状态。格子里点一下就同时锁住两维。 */}
          <div className="overflow-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('managedMatrix.colConfiguration')}</TableHead>
                  {CONNECTIONS.map((conn) => (
                    <TableHead key={conn}>{t(`managedMatrix.connection.${conn}`)}</TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {CONFIGURATIONS.map((conf) => (
                  <TableRow key={conf}>
                    <TableCell className="font-medium">{t(`managedMatrix.configuration.${conf}`)}</TableCell>
                    {CONNECTIONS.map((conn) => {
                      const count = cellCount(summary?.crossMatrix ?? [], conf, conn)
                      const active = configuration === conf && connection === conn
                      return (
                        <TableCell key={conn}>
                          <Button
                            type="button"
                            size="xs"
                            variant={active ? 'default' : 'ghost'}
                            disabled={count === 0}
                            onClick={() =>
                              applyFilter(() => {
                                setConfiguration(active ? '' : conf)
                                setConnection(active ? '' : conn)
                              })
                            }
                            data-testid={`managed-matrix-cell-${conf}-${conn}`}
                          >
                            {count}
                          </Button>
                        </TableCell>
                      )
                    })}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </div>
      </Panel>

      <Panel
        title={t('managedMatrix.listTitle')}
        icon={<Table2 className="size-4" />}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Input
              className="h-8 w-40"
              value={keyword}
              placeholder={t('managedMatrix.searchPlaceholder')}
              onChange={(e) => applyFilter(() => setKeyword(e.target.value))}
              data-testid="managed-matrix-search"
            />
            <NativeSelect
              size="sm"
              aria-label={t('managedMatrix.colNode')}
              value={nodeId || ''}
              onChange={(e) => applyFilter(() => setNodeId(Number(e.target.value)))}
              data-testid="managed-matrix-node"
            >
              <NativeSelectOption value="">{t('managedMatrix.allNodes')}</NativeSelectOption>
              {(nodesQuery.data ?? []).map((node) => (
                <NativeSelectOption key={node.id} value={node.id}>
                  {node.name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <NativeSelect
              size="sm"
              aria-label={t('managedMatrix.colConnection')}
              value={connection}
              onChange={(e) => applyFilter(() => setConnection(e.target.value))}
              data-testid="managed-matrix-connection"
            >
              <NativeSelectOption value="">{t('common.all')}</NativeSelectOption>
              {CONNECTIONS.map((conn) => (
                <NativeSelectOption key={conn} value={conn}>
                  {t(`managedMatrix.connection.${conn}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <NativeSelect
              size="sm"
              aria-label={t('managedMatrix.colConfiguration')}
              value={configuration}
              onChange={(e) => applyFilter(() => setConfiguration(e.target.value))}
              data-testid="managed-matrix-configuration"
            >
              <NativeSelectOption value="">{t('common.all')}</NativeSelectOption>
              {CONFIGURATIONS.map((conf) => (
                <NativeSelectOption key={conf} value={conf}>
                  {t(`managedMatrix.configuration.${conf}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>
        }
        footer={
          total > 0 ? (
            <PaginationBar
              page={page}
              totalPages={totalPages}
              total={total}
              onChange={setPage}
              totalLabel={t('managedMatrix.totalLabel', { total })}
              prevLabel={t('common.prevPage')}
              nextLabel={t('common.nextPage')}
            />
          ) : undefined
        }
      >
        {matrixQuery.isError ? (
          <p className="text-sm text-destructive" data-testid="managed-matrix-error">
            {t('managedMatrix.loadFailed')}
          </p>
        ) : matrixQuery.isLoading ? (
          <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : rows.length === 0 ? (
          <EmptyState
            title={t('managedMatrix.empty')}
            description={consistency ? t('managedMatrix.emptyFiltered') : t('managedMatrix.emptyHint')}
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('managedMatrix.colInstance')}</TableHead>
                <TableHead>{t('managedMatrix.colNode')}</TableHead>
                <TableHead>{t('managedMatrix.colDesired')}</TableHead>
                <TableHead>{t('managedMatrix.colObserved')}</TableHead>
                <TableHead>{t('managedMatrix.colConfiguration')}</TableHead>
                <TableHead>{t('managedMatrix.colConnection')}</TableHead>
                <TableHead>{t('managedMatrix.colCacheAge')}</TableHead>
                <TableHead>{t('managedMatrix.colConsistency')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row: ManagedMatrixRow) => {
                const staleNote = staleBridgeNote(row, formatRelativeTime(row.reportedAt))
                return (
                  <TableRow key={row.instanceId} data-testid="managed-matrix-row">
                    <TableCell>
                      <Button
                        type="button"
                        size="xs"
                        variant="link"
                        onClick={() => setDetailId(row.instanceId)}
                        data-testid={`managed-matrix-open-${row.instanceId}`}
                      >
                        {row.instanceName}
                      </Button>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{row.nodeName || `#${row.nodeId}`}</TableCell>
                    {/* null = 未下发；显示 0 会把「从未下发」说成「下发了初始版本」。 */}
                    <TableCell className="tabular-nums" data-testid="managed-desired">
                      {formatDesiredVersion(row.desiredVersion, t('managedMatrix.notPushed'))}
                    </TableCell>
                    <TableCell className="tabular-nums" data-testid="managed-observed">
                      {formatObservedVersion(row.observedVersion, t('managedMatrix.neverReportedVersion'))}
                    </TableCell>
                    <TableCell className="text-xs">{t(`managedMatrix.configuration.${row.configuration}`)}</TableCell>
                    <TableCell>
                      <div className="flex flex-col gap-0.5">
                        <StatusBadge
                          level={connectionLevel(row.connection)}
                          label={t(`managedMatrix.connection.${row.connection}`)}
                        />
                        {/* 断连 + 有上报时刻 = 自报值已陈旧，必须说清「最后上报于何时」。 */}
                        {staleNote ? (
                          <span className="text-[11px] text-muted-foreground" data-testid="managed-stale-note">
                            {t('managedMatrix.staleBridge', { ago: staleNote })}
                          </span>
                        ) : null}
                      </div>
                    </TableCell>
                    {/* null = 从未成功应用；显示 0 会把「没生效过」说成「刚刚生效」。 */}
                    <TableCell className="tabular-nums" data-testid="managed-cache-age">
                      {formatCacheAge(row.cacheAgeSeconds, t('managedMatrix.noCache'))}
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-col gap-0.5">
                        <StatusBadge
                          level={consistencyLevel(row.consistency)}
                          label={t(`managedMatrix.consistency.${row.consistency}`)}
                        />
                        {row.lastPushError ? (
                          <span className="max-w-[220px] truncate text-[11px] text-destructive" title={row.lastPushError}>
                            {row.lastPushError}
                          </span>
                        ) : null}
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </Panel>

      {/* 全局提示：把「一致性判定的优先级」写清楚，避免 failed 被读成 neverReported。 */}
      <Alert variant="info" className="px-3 py-2">
        <GitCompareArrows aria-hidden />
        <AlertDescription className="text-xs">{t('managedMatrix.legend')}</AlertDescription>
      </Alert>

      <InstanceDetailSheet instanceId={detailId} onClose={() => setDetailId(null)} />
    </PageShell>
  )
}
