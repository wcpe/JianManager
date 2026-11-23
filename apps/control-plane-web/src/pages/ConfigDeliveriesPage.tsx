import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { Workflow } from 'lucide-react'
import { Button } from '@jianmanager/ui/components/button'
import { Panel } from '@jianmanager/ui/components/panel'
import { PageHeader, PageShell } from '@jianmanager/ui/components/layout'
import { StatusBadge } from '@jianmanager/ui/components/status-badge'
import { NativeSelect } from '@jianmanager/ui/components/native-select'
import { PaginationBar } from '@jianmanager/ui/components/pagination'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import {
  DELIVERY_STATUS_KEYS,
  deliveryStatusLevel,
  isCircuitBreakPaused,
  useConfigDeliveries,
  type ConfigDeliveryOrder,
  type DeliveryStatus,
} from '@/api/configDeliveries'

/** 
 * 配置交付编排列表页。
 *
 * 【列表要一次答清的三个问题】运维打开这一页是在出事之后：这次变更**停在哪**、
 * **多少台成功/失败**、**有几台只是离线挂着**。
 *
 * 因此每行必须把 `failedCount` 与 `deferredCount` **分两列显示**——合并成一个「异常数」
 * 会让人以为离线机器是配置坏了；而熔断停下的单（`paused` + `pauseKind=circuit_break`）
 * 也单独标注，否则它看起来和「人工暂停等人继续」完全一样。
 */

/** 状态筛选项顺序：按变更生命周期排，不是字母序。 */
const STATUS_FILTERS: (DeliveryStatus | '')[] = [
  '',
  'draft',
  'pending_approval',
  'approved',
  'rolling',
  'observing',
  'paused',
  'failed',
  'completed',
  'rolled_back',
  'cancelled',
]

/** 把 ISO 时间压成本地可读形态；空值显示占位符。 */
function formatTime(value: string | null | undefined): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString()
}

/** 单行摘要：单号 / 状态 / 目标数 / 成功失败 / 创建时间。 */
function DeliveryRow({ order }: { order: ConfigDeliveryOrder }) {
  const { t } = useTranslation()
  const circuitBreak = isCircuitBreakPaused(order)
  return (
    <TableRow data-testid="delivery-row">
      <TableCell>
        <Link to={`/config-deliveries/${order.id}`} className="font-medium hover:underline">
          #{order.id} {order.filePath}
        </Link>
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-1">
          <StatusBadge
            level={deliveryStatusLevel(order.status)}
            label={t(`configDeliveries.status.${DELIVERY_STATUS_KEYS[order.status]}`)}
            pulse={order.status === 'rolling' || order.status === 'rolling_back'}
          />
          {/* 熔断停下的单必须与人工暂停区分：前者是系统替人踩了刹车，后者是人在等。 */}
          {circuitBreak ? (
            <StatusBadge level="danger" label={t('configDeliveries.circuitBreakBadge')} dot={false} />
          ) : null}
        </div>
      </TableCell>
      <TableCell className="tabular-nums">{order.plannedCount}</TableCell>
      <TableCell className="tabular-nums" data-testid="delivery-activated">
        {order.activatedCount}
      </TableCell>
      {/* 失败与离线挂起分两列：deferred 不计入失败率，合并显示会误判为「配置坏了」。 */}
      <TableCell className="tabular-nums" data-testid="delivery-failed">
        {order.failedCount}
      </TableCell>
      <TableCell className="tabular-nums text-muted-foreground" data-testid="delivery-deferred">
        {order.deferredCount}
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">{formatTime(order.createdAt)}</TableCell>
    </TableRow>
  )
}

export default function ConfigDeliveriesPage() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<DeliveryStatus | ''>('')
  const [page, setPage] = useState(1)
  const listQ = useConfigDeliveries({ status: status || undefined, page, pageSize: 20 })

  const orders = listQ.data?.orders ?? []
  const total = listQ.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / (listQ.data?.pageSize ?? 20)))

  return (
    <PageShell>
      <PageHeader
        title={t('configDeliveries.title')}
        count={total}
        description={t('configDeliveries.description')}
      />

      <Panel
        title={t('configDeliveries.listTitle')}
        icon={<Workflow className="size-4" />}
        actions={
          <div className="flex items-center gap-2">
            <NativeSelect
              aria-label={t('common.status')}
              data-testid="delivery-status-filter"
              value={status}
              onChange={(e) => {
                setStatus(e.target.value as DeliveryStatus | '')
                setPage(1)
              }}
            >
              {STATUS_FILTERS.map((s) => (
                <option key={s || 'all'} value={s}>
                  {s ? t(`configDeliveries.status.${DELIVERY_STATUS_KEYS[s]}`) : t('common.all')}
                </option>
              ))}
            </NativeSelect>
            <Button size="sm" variant="outline" disabled={listQ.isFetching} onClick={() => { void listQ.refetch() }}>
              {t('common.refresh')}
            </Button>
          </div>
        }
        footer={
          total > 0 ? (
            <PaginationBar
              page={page}
              totalPages={totalPages}
              total={total}
              onChange={setPage}
              totalLabel={t('configDeliveries.totalLabel', { total })}
              prevLabel={t('common.prevPage')}
              nextLabel={t('common.nextPage')}
            />
          ) : undefined
        }
      >
        {listQ.isError ? (
          // 取数失败必须与「还没有交付单」区分：否则权限/后端故障会被读成「没做过变更」。
          <p className="text-sm text-destructive" data-testid="delivery-list-error">
            {t('configDeliveries.loadFailed')}
          </p>
        ) : listQ.isLoading ? (
          <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : orders.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="delivery-list-empty">
            {t('configDeliveries.empty')}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('configDeliveries.colTitle')}</TableHead>
                <TableHead>{t('common.status')}</TableHead>
                <TableHead>{t('configDeliveries.colPlanned')}</TableHead>
                <TableHead>{t('configDeliveries.colActivated')}</TableHead>
                <TableHead>{t('configDeliveries.colFailed')}</TableHead>
                <TableHead>{t('configDeliveries.colDeferred')}</TableHead>
                <TableHead>{t('configDeliveries.colCreatedAt')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {orders.map((order) => (
                <DeliveryRow key={order.id} order={order} />
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
    </PageShell>
  )
}
