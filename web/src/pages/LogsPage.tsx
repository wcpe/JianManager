import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useLogs, exportLogs, type LogQueryParams } from '@/api/logs'
import { useNodes } from '@/api/nodes'
import { useInstances } from '@/api/instances'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

// Radix Select 不允许空字符串值，用哨兵代表「全部」。
const SENTINEL_ALL = '__all__'
const PAGE_SIZE = 50
const SOURCES = ['instance', 'control_plane', 'worker']
const LEVELS = ['debug', 'info', 'warn', 'error']

/** 日志级别 → Badge 变体（错误红 / 警告与 debug 弱化 / info 默认）。 */
function levelVariant(level: string): 'default' | 'destructive' | 'secondary' | 'outline' {
  switch (level) {
    case 'error':
      return 'destructive'
    case 'warn':
      return 'secondary'
    case 'debug':
      return 'outline'
    default:
      return 'default'
  }
}

/**
 * 日志中心（FR-049 最小查看页 / FR-050 完整检索）。
 * 筛选（来源/级别/节点/实例/关键字）下沉到后端 DB，分页加载，不一次性渲染全部；支持导出。
 */
export default function LogsPage() {
  const { t } = useTranslation()
  const { data: nodes } = useNodes()
  const { data: instances } = useInstances()

  const [source, setSource] = useState('')
  const [level, setLevel] = useState('')
  const [nodeId, setNodeId] = useState<number | null>(null)
  const [instanceId, setInstanceId] = useState<number | null>(null)
  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  const [exporting, setExporting] = useState(false)

  const params: LogQueryParams = {
    page,
    pageSize: PAGE_SIZE,
    ...(source ? { source } : {}),
    ...(level ? { level } : {}),
    ...(nodeId !== null ? { nodeId } : {}),
    ...(instanceId !== null ? { instanceId } : {}),
    ...(keyword.trim() ? { keyword: keyword.trim() } : {}),
  }

  const { data, isLoading, isError } = useLogs(params)

  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  // 改任一筛选都回到第 1 页，避免停留在越界页。
  const resetTo = <T,>(setter: (v: T) => void) => (v: T) => {
    setter(v)
    setPage(1)
  }

  const handleExport = async () => {
    setExporting(true)
    try {
      await exportLogs(params)
      toast.success(t('logs.exportStarted'))
    } catch {
      toast.error(t('logs.exportFailed'))
    } finally {
      setExporting(false)
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">{t('logs.title')}</h1>
        <Button variant="outline" size="sm" onClick={handleExport} disabled={exporting || total === 0}>
          {exporting ? t('logs.exporting') : t('logs.export')}
        </Button>
      </div>

      {/* 筛选器 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          value={keyword}
          onChange={(e) => resetTo(setKeyword)(e.target.value)}
          placeholder={t('logs.searchPlaceholder')}
          className="h-9 w-56"
        />
        <Select
          value={source === '' ? SENTINEL_ALL : source}
          onValueChange={(v: string) => resetTo(setSource)(v === SENTINEL_ALL ? '' : v)}
        >
          <SelectTrigger size="sm" className="w-36">
            <SelectValue placeholder={t('logs.allSources')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('logs.allSources')}</SelectItem>
            {SOURCES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`logs.source_${s}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={level === '' ? SENTINEL_ALL : level}
          onValueChange={(v: string) => resetTo(setLevel)(v === SENTINEL_ALL ? '' : v)}
        >
          <SelectTrigger size="sm" className="w-32">
            <SelectValue placeholder={t('logs.allLevels')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('logs.allLevels')}</SelectItem>
            {LEVELS.map((l) => (
              <SelectItem key={l} value={l}>
                {t(`logs.level_${l}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={nodeId === null ? SENTINEL_ALL : String(nodeId)}
          onValueChange={(v: string) => resetTo(setNodeId)(v === SENTINEL_ALL ? null : Number(v))}
        >
          <SelectTrigger size="sm" className="w-40">
            <SelectValue placeholder={t('logs.allNodes')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('logs.allNodes')}</SelectItem>
            {nodes?.map((node) => (
              <SelectItem key={node.id} value={String(node.id)}>
                {node.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={instanceId === null ? SENTINEL_ALL : String(instanceId)}
          onValueChange={(v: string) => resetTo(setInstanceId)(v === SENTINEL_ALL ? null : Number(v))}
        >
          <SelectTrigger size="sm" className="w-48">
            <SelectValue placeholder={t('logs.allInstances')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={SENTINEL_ALL}>{t('logs.allInstances')}</SelectItem>
            {instances?.map((inst) => (
              <SelectItem key={inst.id} value={String(inst.id)}>
                {inst.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {isLoading ? (
        <p className="text-muted-foreground">{t('common.loading')}</p>
      ) : isError ? (
        <p className="text-destructive">{t('logs.loadError')}</p>
      ) : (
        <>
          <div className="border rounded-lg overflow-hidden">
            <Table>
              <TableHeader className="bg-muted/50">
                <TableRow>
                  <TableHead className="w-44">{t('logs.time')}</TableHead>
                  <TableHead className="w-20">{t('logs.level')}</TableHead>
                  <TableHead className="w-28">{t('logs.source')}</TableHead>
                  <TableHead>{t('logs.message')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data?.items.map((log) => (
                  <TableRow key={log.id}>
                    <TableCell className="text-muted-foreground whitespace-nowrap font-mono text-xs">
                      {new Date(log.time).toLocaleString()}
                    </TableCell>
                    <TableCell>
                      <Badge variant={levelVariant(log.level)}>{log.level}</Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">
                      {t(`logs.source_${log.source}`, log.source)}
                    </TableCell>
                    <TableCell className="font-mono text-xs break-all">{log.message}</TableCell>
                  </TableRow>
                ))}
                {(!data || data.items.length === 0) && (
                  <TableRow>
                    <TableCell colSpan={4} className="text-center text-muted-foreground">
                      {t('logs.empty')}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>

          {/* 分页 */}
          <div className="flex items-center justify-between text-sm text-muted-foreground">
            <span>{t('logs.totalCount', { count: total })}</span>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={page <= 1}
                onClick={() => setPage((p) => Math.max(1, p - 1))}
              >
                {t('logs.prevPage')}
              </Button>
              <span>{t('logs.pageInfo', { page, totalPages })}</span>
              <Button
                variant="outline"
                size="sm"
                disabled={page >= totalPages}
                onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              >
                {t('logs.nextPage')}
              </Button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
