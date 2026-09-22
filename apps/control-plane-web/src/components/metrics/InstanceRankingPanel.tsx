/* eslint-disable react-refresh/only-export-components -- 与组件同文件导出格式化纯函数，供单测直接断言（仓库既有约定） */
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { useNavigate } from 'react-router'
import { Trophy } from 'lucide-react'
import { useInstanceRanking, type RankingItem, type RankingMetric } from '@/api/metrics'
import { Panel } from '@jianmanager/ui/components/panel'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@jianmanager/ui/components/table'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@jianmanager/ui/components/select'
import { fmtBytes } from './format'

/** 可切换的排行指标（与后端 rankingSupportedMetrics 对齐）。 */
const RANKING_METRICS: { value: RankingMetric; labelKey: string }[] = [
  { value: 'inst_tps', labelKey: 'ranking.metricTps' },
  { value: 'inst_mspt', labelKey: 'ranking.metricMspt' },
  { value: 'inst_cpu_pct', labelKey: 'ranking.metricCpu' },
  { value: 'inst_heap_used', labelKey: 'ranking.metricHeap' },
  { value: 'inst_players_online', labelKey: 'ranking.metricPlayers' },
]

/** 周期窗口选项（代表值 = 窗口内均值，比最新一拍稳健）。 */
const RANKING_WINDOWS = ['5m', '1h', '24h', '7d'] as const

/**
 * 代表值格式化：按指标量纲选口径（不把字节当计数渲染）。
 *
 * 带单位的三种走 i18n 格式键（`ranking.unitMs/unitTps/unitPct`），使**单位可本地化**
 * ——原先硬编码 `` `${v.toFixed(1)} ms` `` 会让这些键成为孤儿，且单位在非英文语境下无法调整（自审 M6）。
 * 首行 `isFinite` 防护与本文件 `fmtBytes` 一致：`NaN`/`Infinity` 不得渲染成字面量（自审 m2）。
 */
export function fmtRankingValue(metric: RankingMetric, v: number, t: TFunction): string {
  if (!Number.isFinite(v)) return '--'
  switch (metric) {
    case 'inst_tps':
      return t('ranking.unitTps', { value: v.toFixed(1) })
    case 'inst_mspt':
      return t('ranking.unitMs', { value: v.toFixed(1) })
    case 'inst_cpu_pct':
      return t('ranking.unitPct', { value: v.toFixed(1) })
    case 'inst_heap_used':
      return fmtBytes(v)
    case 'inst_players_online':
    default:
      return v.toFixed(0)
  }
}

/** 名次徽标样式：前三名主色强调，其余中性。 */
function rankClass(rank: number): string {
  return rank <= 3 ? 'font-semibold text-primary' : 'text-muted-foreground'
}

function fmtSampledAt(value: string): string {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? '--' : d.toLocaleString()
}

/** 全局排行面板的 props。 */
interface InstanceRankingPanelProps {
  /**
   * 节点列表（uuid → 名称映射），用于「节点」列与节点筛选下拉。
   * 缺省时「节点」列回退显示 `nodeUuid` 前 8 位，筛选下拉只剩「全部」。
   */
  nodes?: { uuid: string; name: string }[]
}

/**
 * 全局排行表（FR-469）：跨节点全量实例按指标窗口代表值排序，带名次与代表值。
 * 权限：非管理员只见可访问实例（后端 scoped 标识），UI 显式提示受限视图。
 * 点击行下钻到实例详情。窗口内无数据的实例不入榜（后端 skippedNoData 计数提示）。
 */
export function InstanceRankingPanel({ nodes }: InstanceRankingPanelProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [metric, setMetric] = useState<RankingMetric>('inst_tps')
  const [window, setWindow] = useState<(typeof RANKING_WINDOWS)[number]>('5m')
  const [nodeId, setNodeId] = useState<string>('all')
  const { data, isError, isLoading } = useInstanceRanking({
    metric,
    window,
    limit: 50,
    nodeId: nodeId === 'all' ? undefined : nodeId,
  })

  const items = data?.items ?? []
  const nodeName = (uuid: string) => nodes?.find((n) => n.uuid === uuid)?.name ?? uuid.slice(0, 8)

  return (
    <Panel
      title={t('ranking.title')}
      icon={<Trophy className="size-4" />}
      actions={
        <div className="flex items-center gap-2">
          <Select value={metric} onValueChange={(v) => setMetric(v as RankingMetric)}>
            <SelectTrigger size="sm" className="w-[136px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RANKING_METRICS.map((m) => (
                <SelectItem key={m.value} value={m.value}>
                  {t(m.labelKey)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={window} onValueChange={(v) => setWindow(v as (typeof RANKING_WINDOWS)[number])}>
            <SelectTrigger size="sm" className="w-[84px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RANKING_WINDOWS.map((w) => (
                <SelectItem key={w} value={w}>
                  {w}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={nodeId} onValueChange={setNodeId}>
            <SelectTrigger size="sm" className="w-[140px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t('grouping.all')}</SelectItem>
              {(nodes ?? []).map((n) => (
                <SelectItem key={n.uuid} value={n.uuid}>
                  {n.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      }
    >
      {data?.scoped && <p className="mb-2 text-[11px] text-muted-foreground">{t('ranking.scoped')}</p>}
      {isError ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('ranking.error')}</p>
      ) : isLoading ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('ranking.loading')}</p>
      ) : items.length === 0 ? (
        <p className="py-5 text-center text-sm text-muted-foreground">{t('ranking.empty')}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-14">{t('ranking.rank')}</TableHead>
              <TableHead>{t('ranking.instance')}</TableHead>
              <TableHead>{t('ranking.node')}</TableHead>
              <TableHead className="text-right">{t('ranking.value')}</TableHead>
              <TableHead className="text-right">{t('ranking.sampledAt')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((row: RankingItem) => (
              <TableRow
                key={row.instanceUuid}
                tabIndex={0}
                data-testid="ranking-row"
                className="cursor-pointer"
                onClick={() => navigate(`/instances/${row.instanceId}`)}
              >
                <TableCell className={`tabular-nums ${rankClass(row.rank)}`}>{row.rank}</TableCell>
                <TableCell className="font-medium">{row.name}</TableCell>
                <TableCell className="text-muted-foreground">{nodeName(row.nodeUuid)}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{fmtRankingValue(metric, row.value, t)}</TableCell>
                <TableCell className="text-right text-[11px] text-muted-foreground">{fmtSampledAt(row.sampledAt)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {!!data?.skippedNoData && (
        <p className="mt-2 text-[11px] text-muted-foreground">{t('ranking.skipped', { count: data.skippedNoData })}</p>
      )}
    </Panel>
  )
}
