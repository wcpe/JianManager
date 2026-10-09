/**
 * @file DatabaseRowsView：数据库只读行浏览的受控视图，取数与查询变更由应用容器负责。
 * @input lib/db-contracts、lib/db-rows-view、lib/utils、表格与表单原语、翻译上下文
 * @output DatabaseRowsView、DatabaseRowsViewProps、DatabaseRowsState
 * @sync apps/control-plane-web/src/components/database/DatabaseExplorer.tsx、apps/control-plane-web/src/pages/DatabasePage.dom.test.tsx、apps/control-plane-web/src/components/database/rows-view.test.ts
 * @since FR-502
 */
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowDown, ArrowUp, Search } from 'lucide-react'
import type { DbColumn, DbRowsResult } from '@/lib/database/db-contracts'
import { cn } from '@jianmanager/ui/lib/utils'
import { normalizeColumns, normalizeRows, shouldShowEmptyRow } from '@/lib/database/db-rows-view'
import { Input } from '@jianmanager/ui/components/input'
import { Button } from '@jianmanager/ui/components/button'
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

/** 受控行视图的状态；接口请求参数及其可选值映射留在应用容器。 */
export interface DatabaseRowsState {
  page: number
  pageSize: number
  sort: string
  order: 'asc' | 'desc'
  filterColumn: string
  filterValue: string
}

/** 查询状态与数据由容器注入，过滤输入草稿留在视图内，动作通过回调上报。 */
export interface DatabaseRowsViewProps {
  query: DatabaseRowsState
  data?: DbRowsResult
  isLoading: boolean
  isError: boolean
  isFetching: boolean
  onSort: (column: string) => void
  onFilterColumnChange: (column: string) => void
  onApplyFilter: (value: string) => void
  onClearFilter: () => void
  onPageChange: (page: number) => void
  onPageSizeChange: (pageSize: number) => void
}

/** 敏感列前端兜底打码占位（后端应已脱敏，此处双重保险）。 */
const MASKED = '******'

/** Radix Select 不接受空字符串值，用哨兵代表「不过滤任何列」。 */
const NO_FILTER_COLUMN = '__none__'

/** 每页行数选项。 */
const PAGE_SIZES = [25, 50, 100, 200] as const

/**
 * 把单元格值渲染为可读文本：null/undefined → 空占位；对象 → JSON；其余 → String。
 * 敏感列由调用方在外层替换为打码占位，不在此处理。
 */
function renderCell(v: unknown): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

/**
 * 单表行浏览：顶部过滤条（选列 + 关键字）+ 点击列头排序 + 底部分页器。
 * 切表时由应用容器按活动表重建，重置查询状态及本地过滤草稿。
 */
export function DatabaseRowsView({
  query,
  data,
  isLoading,
  isError,
  isFetching,
  onSort,
  onFilterColumnChange,
  onApplyFilter,
  onClearFilter,
  onPageChange,
  onPageSizeChange,
}: DatabaseRowsViewProps) {
  const { t } = useTranslation()
  const { page, pageSize, sort, order, filterColumn, filterValue } = query
  const [filterValueDraft, setFilterValueDraft] = useState('')

  const columns = useMemo(() => normalizeColumns(data), [data])
  const sensitive = useMemo(() => {
    const s = new Set<string>()
    for (const c of columns) if (c.sensitive) s.add(c.name)
    return s
  }, [columns])
  const filterableColumns = useMemo(() => columns.filter((c) => !c.sensitive), [columns])

  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  const applyFilter = () => {
    onApplyFilter(filterValueDraft.trim())
  }

  const clearFilter = () => {
    setFilterValueDraft('')
    onClearFilter()
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      {/* 过滤条 */}
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={filterColumn === '' ? NO_FILTER_COLUMN : filterColumn}
          onValueChange={(v: string) => {
            onFilterColumnChange(v === NO_FILTER_COLUMN ? '' : v)
          }}
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder={t('database.filterColumn')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NO_FILTER_COLUMN}>{t('database.noFilter')}</SelectItem>
            {filterableColumns.map((c) => (
              <SelectItem key={c.name} value={c.name}>
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Input
          value={filterValueDraft}
          onChange={(e) => setFilterValueDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') applyFilter()
          }}
          placeholder={t('database.filterValue')}
          disabled={!filterColumn}
          className="h-9 w-56"
        />
        <Button size="sm" variant="outline" onClick={applyFilter} disabled={!filterColumn}>
          <Search className="size-3.5" />
          {t('database.apply')}
        </Button>
        {(filterColumn || filterValue) && (
          <Button size="sm" variant="ghost" onClick={clearFilter}>
            {t('database.clear')}
          </Button>
        )}
        <span className="ml-auto text-xs text-muted-foreground">
          {t('database.totalRows', { count: total })}
          {isFetching && <span className="ml-2 opacity-60">{t('common.loading')}</span>}
        </span>
      </div>

      {/* 行表格 */}
      <div className="min-h-0 flex-1 overflow-auto rounded-lg border">
        {isLoading ? (
          <p className="p-4 text-sm text-muted-foreground">{t('common.loading')}</p>
        ) : isError ? (
          <p className="p-4 text-sm text-destructive">{t('database.rowsError')}</p>
        ) : (
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-muted/80 backdrop-blur">
              <TableRow>
                {columns.map((c) => (
                  <ColumnHead
                    key={c.name}
                    column={c}
                    sorted={sort === c.name ? order : null}
                    sortable={!c.sensitive}
                    onClick={() => onSort(c.name)}
                  />
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {normalizeRows(data).map((row, i) => (
                <TableRow key={i}>
                  {columns.map((c) => (
                    <TableCell key={c.name} className="max-w-[24rem] truncate font-mono text-xs">
                      {sensitive.has(c.name)
                        ? row[c.name] == null
                          ? ''
                          : MASKED
                        : renderCell(row[c.name])}
                    </TableCell>
                  ))}
                </TableRow>
              ))}
              {shouldShowEmptyRow(data) && (
                <TableRow>
                  <TableCell
                    colSpan={Math.max(1, columns.length)}
                    className="py-8 text-center text-sm text-muted-foreground"
                  >
                    {t('database.noRows')}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        )}
      </div>

      {/* 分页器 */}
      <div className="flex items-center justify-between gap-3 text-xs text-muted-foreground">
        <div className="flex items-center gap-2">
          <span>{t('database.pageSize')}</span>
          <Select
            value={String(pageSize)}
            onValueChange={(v: string) => {
              onPageSizeChange(Number(v))
            }}
          >
            <SelectTrigger size="sm" className="w-20">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PAGE_SIZES.map((sz) => (
                <SelectItem key={sz} value={String(sz)}>
                  {sz}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex items-center gap-3">
          <span>{t('database.pageOf', { page, total: totalPages })}</span>
          <div className="flex items-center gap-1">
            <Button
              size="sm"
              variant="outline"
              disabled={page <= 1}
              onClick={() => onPageChange(Math.max(1, page - 1))}
            >
              {t('database.prev')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={page >= totalPages}
              onClick={() => onPageChange(page + 1)}
            >
              {t('database.next')}
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

/** 列头：列名 + 排序指示（点击切换），敏感列以 monospace 弱化标注。 */
function ColumnHead({
  column,
  sorted,
  sortable,
  onClick,
}: {
  column: DbColumn
  sorted: 'asc' | 'desc' | null
  sortable: boolean
  onClick: () => void
}) {
  const { t } = useTranslation()

  return (
    <TableHead
      aria-disabled={!sortable}
      className={cn(
        'select-none',
        sortable ? 'cursor-pointer hover:text-foreground' : 'cursor-not-allowed opacity-70',
      )}
      onClick={sortable ? onClick : undefined}
    >
      <span className="inline-flex items-center gap-1">
        <span className="font-mono">{column.name}</span>
        {column.sensitive && (
          <span className="rounded border border-status-warning/40 bg-status-warning/10 px-1 py-0.5 text-[10px] font-medium text-status-warning">
            {t('database.sensitiveColumn')}
          </span>
        )}
        {sorted === 'asc' && <ArrowUp className="size-3" />}
        {sorted === 'desc' && <ArrowDown className="size-3" />}
      </span>
    </TableHead>
  )
}
