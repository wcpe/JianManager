import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowDown, ArrowUp, Database, Search } from 'lucide-react'
import {
  useDbTables,
  useDbTableRows,
  type DbColumn,
  type DbRowsParams,
} from '@/api/db'
import { cn } from '@/lib/utils'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
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
 * 数据库资源管理器（FR-084）：左表树 + 右行只读浏览。
 * 视觉/交互对齐 FR-070 资源管理器（左树右内容），但因 ResourceExplorer 与文件 gRPC 强耦合、
 * 不可改其本体，故为独立轻量组件。仅平台管理员可达（入口收敛 + 后端 RBAC 双重把关）。
 * 切表/翻页/排序/过滤均走后端（仅请求当前页），大表分页不卡；只读，无任何编辑入口。
 */
export default function DatabaseExplorer() {
  const { t } = useTranslation()
  const { data: tables, isLoading: tablesLoading, isError: tablesError } = useDbTables()

  const [selected, setSelected] = useState<string>('')
  // 当前选中表无效（首次加载/删表）时回退到首个表。
  const activeTable = useMemo(() => {
    if (selected && tables?.some((tb) => tb.name === selected)) return selected
    return tables?.[0]?.name ?? ''
  }, [selected, tables])

  return (
    <div className="flex min-h-0 flex-1 gap-4">
      {/* 左：表树 */}
      <aside className="flex w-56 shrink-0 flex-col rounded-lg border bg-card/40">
        <div className="flex items-center gap-2 border-b px-3 py-2 text-sm font-medium">
          <Database className="size-4" />
          <span>{t('database.tables')}</span>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto p-1">
          {tablesLoading && (
            <p className="px-2 py-3 text-xs text-muted-foreground">{t('common.loading')}</p>
          )}
          {tablesError && (
            <p className="px-2 py-3 text-xs text-destructive">{t('database.tablesError')}</p>
          )}
          {!tablesLoading && !tablesError && (tables?.length ?? 0) === 0 && (
            <p className="px-2 py-3 text-xs text-muted-foreground">{t('database.noTables')}</p>
          )}
          {tables?.map((tb) => (
            <button
              key={tb.name}
              type="button"
              onClick={() => setSelected(tb.name)}
              className={cn(
                'flex w-full items-center justify-between gap-2 rounded px-2 py-1.5 text-left text-[13px] transition-colors',
                tb.name === activeTable
                  ? 'bg-primary/10 font-medium text-primary'
                  : 'text-foreground/80 hover:bg-accent/60',
              )}
            >
              <span className="truncate font-mono">{tb.name}</span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {tb.rowCount < 0 ? '?' : tb.rowCount}
              </span>
            </button>
          ))}
        </div>
      </aside>

      {/* 右：行浏览 */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        {activeTable ? (
          <TableRowsView key={activeTable} table={activeTable} />
        ) : (
          <div className="grid flex-1 place-items-center text-sm text-muted-foreground">
            {t('database.selectTable')}
          </div>
        )}
      </div>
    </div>
  )
}

/**
 * 单表行浏览：顶部过滤条（选列 + 关键字）+ 点击列头排序 + 底部分页器。
 * 切表时由 key 重置内部分页/排序/过滤状态（见父组件以 table 作 key 渲染本组件）。
 */
function TableRowsView({ table }: { table: string }) {
  const { t } = useTranslation()

  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState<number>(50)
  const [sort, setSort] = useState('')
  const [order, setOrder] = useState<'asc' | 'desc'>('asc')
  const [filterColumn, setFilterColumn] = useState('')
  const [filterValueDraft, setFilterValueDraft] = useState('')
  const [filterValue, setFilterValue] = useState('')

  const params: DbRowsParams = {
    page,
    pageSize,
    sort: sort || undefined,
    order: sort ? order : undefined,
    filterColumn: filterColumn || undefined,
    filterValue: filterColumn && filterValue ? filterValue : undefined,
  }

  const { data, isLoading, isError, isFetching } = useDbTableRows(table, params)

  const columns = useMemo(() => data?.columns ?? [], [data])
  const sensitive = useMemo(() => {
    const s = new Set<string>()
    for (const c of columns) if (c.sensitive) s.add(c.name)
    return s
  }, [columns])

  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  // 点击列头切换排序：未排序→asc；同列 asc→desc；同列 desc→取消排序。翻回第一页。
  const toggleSort = (col: string) => {
    setPage(1)
    if (sort !== col) {
      setSort(col)
      setOrder('asc')
    } else if (order === 'asc') {
      setOrder('desc')
    } else {
      setSort('')
    }
  }

  const applyFilter = () => {
    setPage(1)
    setFilterValue(filterValueDraft.trim())
  }

  const clearFilter = () => {
    setPage(1)
    setFilterColumn('')
    setFilterValueDraft('')
    setFilterValue('')
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      {/* 过滤条 */}
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={filterColumn === '' ? NO_FILTER_COLUMN : filterColumn}
          onValueChange={(v: string) => {
            setFilterColumn(v === NO_FILTER_COLUMN ? '' : v)
            setPage(1)
            if (v === NO_FILTER_COLUMN) setFilterValue('')
          }}
        >
          <SelectTrigger size="sm" className="w-44">
            <SelectValue placeholder={t('database.filterColumn')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NO_FILTER_COLUMN}>{t('database.noFilter')}</SelectItem>
            {columns.map((c) => (
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
                    onClick={() => toggleSort(c.name)}
                  />
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {(data?.rows ?? []).map((row, i) => (
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
              {(!data || data.rows.length === 0) && (
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
              setPageSize(Number(v))
              setPage(1)
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
              onClick={() => setPage((p) => Math.max(1, p - 1))}
            >
              {t('database.prev')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={page >= totalPages}
              onClick={() => setPage((p) => p + 1)}
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
  onClick,
}: {
  column: DbColumn
  sorted: 'asc' | 'desc' | null
  onClick: () => void
}) {
  return (
    <TableHead className="cursor-pointer select-none" onClick={onClick}>
      <span className="inline-flex items-center gap-1">
        <span className="font-mono">{column.name}</span>
        {sorted === 'asc' && <ArrowUp className="size-3" />}
        {sorted === 'desc' && <ArrowDown className="size-3" />}
      </span>
    </TableHead>
  )
}
