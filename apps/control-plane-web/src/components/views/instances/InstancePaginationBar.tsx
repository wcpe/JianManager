import { useTranslation } from 'react-i18next'

import { Button } from '@jianmanager/ui/components/button'

/**
 * 实例列表的分页条（**列表视图与卡片视图共用**）。
 *
 * 【为什么抽出来】分页条原先写死在 `PaginatedInstanceTable` 内部，只服务表格。
 * 卡片视图（`?view=card`）走的是另一条渲染路径（虚拟分组卡视图），于是：
 * 数据早已改成服务端分页、每页 50 条，但卡片视图既没有分页条、`onNeedMore` 也是空实现
 * （滚动到底不加载）——**第 2 页及之后的实例在卡片视图里根本够不着**。
 * 实测 1200+ 实例时卡片视图只能看到前 28 张（视口内），滚到底也不增加。
 *
 * 抽成共享组件后，两条渲染路径用同一套翻页语义，不会再出现「某一种视图悄悄少一半数据」。
 */
export function InstancePaginationBar({
  loaded,
  total,
  page,
  pageSize,
  isFetching,
  onPageChange,
}: {
  /** 当前页实际渲染的条数（用于「已加载 N / 共 M」里的 N）。 */
  loaded: number
  /** 服务端返回的总数。 */
  total: number
  page: number
  pageSize: number
  /** 翻页请求在途：期间禁用按钮，避免连点穿页。 */
  isFetching?: boolean
  onPageChange: (page: number) => void
}) {
  const { t } = useTranslation()
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  /*
    页码窗口：只渲染当前页附近的 5 个页码。
    1200 条 / 50 = 24 页，全渲染会把分页条撑成两行并挤掉数据区高度。
  */
  const windowStart = Math.max(1, Math.min(page - 2, totalPages - 4))
  const pageNumbers = Array.from({ length: Math.min(5, totalPages) }, (_, i) => windowStart + i)

  return (
    <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
      {/* 总数/当前页与「已加载」合并到同一行：原来它单独占一行 14px，
          在固定视口下等于白吃掉半行数据区高度。 */}
      <span className="tabular-nums" data-testid="instances-loaded-count">
        {t('instances.paginationSummary', {
          defaultValue: '已加载 {{loaded}} / 共 {{total}} 条，第 {{page}} / {{pages}} 页',
          loaded,
          total,
          page,
          pages: totalPages,
        })}
      </span>
      <div className="flex items-center gap-1">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={page <= 1 || isFetching}
          onClick={() => onPageChange(page - 1)}
        >
          {t('common.prevPage', '上一页')}
        </Button>
        {pageNumbers.map((p) => (
          <Button
            key={p}
            type="button"
            variant={p === page ? 'default' : 'outline'}
            size="sm"
            className="min-w-8 tabular-nums"
            disabled={isFetching}
            onClick={() => onPageChange(p)}
            aria-current={p === page ? 'page' : undefined}
          >
            {p}
          </Button>
        ))}
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={page >= totalPages || isFetching}
          onClick={() => onPageChange(page + 1)}
        >
          {t('common.nextPage', '下一页')}
        </Button>
      </div>
    </div>
  )
}
