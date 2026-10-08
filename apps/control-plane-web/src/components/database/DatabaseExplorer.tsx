import { useMemo, useState } from 'react'
import { useDbTables, useDbTableRows, type DbRowsParams } from '@/api/db'
import { DatabaseExplorerView } from '@jianmanager/ui/components/views/database/DatabaseExplorerView'
import { DatabaseRowsView } from '@jianmanager/ui/components/views/database/DatabaseRowsView'

/**
 * 数据库资源管理器（FR-084）容器：取数与查询状态留在应用，展示交共享视图（ADR-097）。
 * 切表/翻页/排序/过滤均走后端，仅请求当前页；页面与后端维持平台管理员权限保护。
 */
export default function DatabaseExplorer() {
  const { data: tables, isLoading: tablesLoading, isError: tablesError } = useDbTables()

  const [selected, setSelected] = useState<string>('')
  // 当前选中表无效（首次加载/删表）时回退到首个表。
  const activeTable = useMemo(() => {
    if (selected && tables?.some((tb) => tb.name === selected)) return selected
    return tables?.[0]?.name ?? ''
  }, [selected, tables])

  return (
    <DatabaseExplorerView
      tables={tables}
      tablesLoading={tablesLoading}
      tablesError={tablesError}
      activeTable={activeTable}
      onSelectTable={setSelected}
    >
      {activeTable ? <TableRowsContainer key={activeTable} table={activeTable} /> : null}
    </DatabaseExplorerView>
  )
}

/** 切表时由 key 重建容器，使查询状态与包内过滤草稿一并恢复初始值。 */
function TableRowsContainer({ table }: { table: string }) {
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState<number>(50)
  const [sort, setSort] = useState('')
  const [order, setOrder] = useState<'asc' | 'desc'>('asc')
  const [filterColumn, setFilterColumn] = useState('')
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

  const applyFilter = (value: string) => {
    setPage(1)
    setFilterValue(value.trim())
  }

  const clearFilter = () => {
    setPage(1)
    setFilterColumn('')
    setFilterValue('')
  }

  return (
    <DatabaseRowsView
      query={{ page, pageSize, sort, order, filterColumn, filterValue }}
      data={data}
      isLoading={isLoading}
      isError={isError}
      isFetching={isFetching}
      onSort={toggleSort}
      onFilterColumnChange={(column) => {
        setFilterColumn(column)
        setPage(1)
        if (column === '') setFilterValue('')
      }}
      onApplyFilter={applyFilter}
      onClearFilter={clearFilter}
      onPageChange={setPage}
      onPageSizeChange={(size) => {
        setPageSize(size)
        setPage(1)
      }}
    />
  )
}
