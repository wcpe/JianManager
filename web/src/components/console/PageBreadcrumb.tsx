import { useTranslation } from 'react-i18next'
import { Link, useLocation } from 'react-router'
import { ChevronRight } from 'lucide-react'

import { useConsoleStore } from '@/stores/console'
import { breadcrumbTrail } from '@/lib/breadcrumb'
import { cn } from '@/lib/utils'

/**
 * 统一页头/面包屑（FR-134 + FR-162）：据当前路由渲染「域 › 页面 [› 末级]」轨迹，
 * 套靛蓝圆角范式（弱化父级、加粗末级、可点节点 hover 高亮）。与 FR-131 五域 IA 对齐。
 *
 * `leaf` 用于补具体末级名称（如打开实例时的实例名）；此时轨迹中的页面节点变为可点回列表。
 * 未知路由（trail 为空）时回退到通用「控制台」标题。
 */
export default function PageBreadcrumb({ leaf }: { leaf?: string }) {
  const { t } = useTranslation()
  const { pathname } = useLocation()
  const closeInstance = useConsoleStore((s) => s.closeInstance)
  const trail = breadcrumbTrail(pathname)

  if (trail.length === 0 && !leaf) {
    return <h1 className="min-w-0 truncate text-sm font-semibold">{t('header.console')}</h1>
  }

  // 末级是否为 leaf：有 leaf 则 leaf 是末级、轨迹全部可点（页面节点已带 to）。
  const items: Array<{ key: string; text: string; to?: string }> = trail.map((c, i) => ({
    key: `${c.labelKey}-${i}`,
    text: t(c.labelKey),
    to: c.to,
  }))
  if (leaf) items.push({ key: 'leaf', text: leaf })

  const lastIdx = items.length - 1

  return (
    <nav aria-label="breadcrumb" className="flex min-w-0 items-center gap-1 text-sm">
      {items.map((it, i) => {
        const isLast = i === lastIdx
        const sep = i > 0 && <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/50" />
        const node =
          it.to && !isLast ? (
            <Link
              key={it.key}
              to={it.to}
              onClick={() => closeInstance()}
              className="shrink-0 truncate text-muted-foreground transition-colors hover:text-foreground"
            >
              {it.text}
            </Link>
          ) : (
            <span
              key={it.key}
              className={cn('truncate', isLast ? 'font-semibold text-foreground' : 'shrink-0 text-muted-foreground')}
            >
              {it.text}
            </span>
          )
        return (
          <span key={it.key} className="flex min-w-0 items-center gap-1">
            {sep}
            {node}
          </span>
        )
      })}
    </nav>
  )
}
