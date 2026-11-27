import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDown, ChevronRight, Coffee, Hexagon, Package, ScrollText, ShieldAlert } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import { Button } from '@jianmanager/ui/components/button'
import {
  summarizeReadiness,
  worstLevel,
  type ReadinessItem,
  type ReadinessLevel,
} from '@/lib/nodes/runtime-readiness'

/**
 * 运行时就绪带：整个「运行时」页签顶部的**结论层**。
 *
 * 【为什么要有这一层】改造前那个页签是三块面板竖排，每块只报原子事实
 * （「3 个 JDK」「Node.js 22」「HOT 未就绪」），用户得自己在三块之间拼出
 * 「这台节点现在能不能干活」。这条带把它压成 4 条带颜色的结论 + 一个主行动，
 * 是整页**唯一**回答「行不行」的地方，内容区因此可以专心做「怎么做」。
 *
 * 【为什么可折叠】KPI + 4 张卡约占 96px，而下面清单表在 1080 视口下一屏只能放 8 行左右。
 * 折叠后只剩一行结论（约 32px），把高度还给表格；选择持久化，与
 * `NodeRuntimeOverview` 的折叠头保持同一交互习惯。
 */
const COLLAPSE_KEY = 'jm.nodes.runtimeStripCollapsed'

function readCollapsed(): boolean {
  try {
    return localStorage.getItem(COLLAPSE_KEY) === '1'
  } catch {
    return false
  }
}

/** 档位 → 卡片着色（半透明状态底 + 状态前景，与健康墙同口径）。 */
const LEVEL_CARD: Record<ReadinessLevel, string> = {
  ok: 'border-status-success/30 bg-status-success/8',
  warn: 'border-status-warning/40 bg-status-warning/10',
  blocked: 'border-status-danger/40 bg-status-danger/10',
  unknown: 'border-border bg-muted/40',
}

/** 档位 → 标题点颜色。 */
const LEVEL_DOT: Record<ReadinessLevel, string> = {
  ok: 'bg-status-success',
  warn: 'bg-status-warning',
  blocked: 'bg-status-danger',
  unknown: 'bg-muted-foreground/50',
}

/** 档位 → 主数字着色。 */
const LEVEL_TEXT: Record<ReadinessLevel, string> = {
  ok: 'text-foreground',
  warn: 'text-status-warning',
  blocked: 'text-status-danger',
  unknown: 'text-muted-foreground',
}

/** 判定 key → 图标（与内容区各分区的图标一致，形成锚点）。 */
const ITEM_ICON = {
  java: Coffee,
  node: Hexagon,
  log: ScrollText,
  packages: Package,
} as const

export interface RuntimeReadinessStripProps {
  items: ReadinessItem[]
  /** 点击某条判定的主行动。 */
  onAction: (key: NonNullable<ReadinessItem['action']>['key']) => void
  className?: string
}

export function RuntimeReadinessStrip({ items, onAction, className }: RuntimeReadinessStripProps) {
  const { t } = useTranslation()
  const [collapsed, setCollapsed] = useState(readCollapsed)

  const toggle = () => {
    setCollapsed((prev) => {
      const next = !prev
      try {
        localStorage.setItem(COLLAPSE_KEY, next ? '1' : '0')
      } catch {
        /* 无存储能力时仅本次会话有效 */
      }
      return next
    })
  }

  const worst = worstLevel(items)
  const summary = summarizeReadiness(items)
  const blockedCount = items.filter((i) => i.level === 'blocked').length
  const warnCount = items.filter((i) => i.level === 'warn').length

  return (
    <section
      className={cn('rounded-lg border bg-card', className)}
      aria-label={t('nodes.runtime.readiness', '运行时就绪')}
      data-testid="runtime-readiness-strip"
    >
      {/* 折叠头：折叠后只剩这一行，用一句话交代结论，避免「收起来就什么都不说」。 */}
      <div className="flex items-center gap-2 px-3 py-1.5">
        <Button
          type="button"
          variant="invisible"
          size="auto"
          onClick={toggle}
          aria-expanded={!collapsed}
          className="inline-flex items-center gap-1 rounded px-1 py-0.5 text-xs font-semibold text-muted-foreground hover:text-foreground"
        >
          {collapsed ? <ChevronRight className="size-3.5" aria-hidden /> : <ChevronDown className="size-3.5" aria-hidden />}
          <ShieldAlert className="size-3.5" aria-hidden />
          {t('nodes.runtime.readiness', '运行时就绪')}
        </Button>
        {/* 折叠态的关键结论；展开态下由卡片自己说，这里只报计数。 */}
        {collapsed ? (
          <span className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <span className={cn('size-1.5 rounded-full', LEVEL_DOT[worst])} aria-hidden />
            {summary}
          </span>
        ) : (
          <span className="flex items-center gap-2 text-[11px] text-muted-foreground tabular-nums">
            {blockedCount > 0 && (
              <span className="text-status-danger">
                {t('nodes.runtime.blockedCount', { count: blockedCount, defaultValue: '{{count}} 项阻塞' })}
              </span>
            )}
            {warnCount > 0 && (
              <span className="text-status-warning">
                {t('nodes.runtime.warnCount', { count: warnCount, defaultValue: '{{count}} 项注意' })}
              </span>
            )}
            {blockedCount === 0 && warnCount === 0 && worst === 'ok' && (
              <span className="text-status-success">{t('nodes.runtime.allReady', '全部就绪')}</span>
            )}
            {blockedCount === 0 && warnCount === 0 && worst === 'unknown' && (
              <span>{t('nodes.runtime.partialUnknown', '部分数据未取到')}</span>
            )}
          </span>
        )}
      </div>

      {!collapsed && (
        <div className="grid gap-1.5 border-t border-border/60 p-2 sm:grid-cols-2 xl:grid-cols-4">
          {items.map((item) => {
            const Icon = ITEM_ICON[item.key]
            return (
              <div
                key={item.key}
                data-testid={`runtime-readiness-${item.key}`}
                data-level={item.level}
                className={cn('flex min-w-0 flex-col gap-0.5 rounded-md border px-2 py-1.5', LEVEL_CARD[item.level])}
              >
                <span className="flex items-center gap-1.5 text-[10px] uppercase tracking-wide text-muted-foreground">
                  <Icon className="size-3 shrink-0" aria-hidden />
                  <span className="truncate">{t(`nodes.runtime.item.${item.key}`, item.key)}</span>
                  <span className={cn('ml-auto size-1.5 shrink-0 rounded-full', LEVEL_DOT[item.level])} aria-hidden />
                </span>
                <span className={cn('truncate text-sm font-semibold leading-tight', LEVEL_TEXT[item.level])} title={item.headline}>
                  {item.headline}
                </span>
                {/*
                  detail 单行截断 + 动作按钮同行：一行 detail 两行会各占 30px，
                  4 张卡就吃掉 176px，把表格挤到首屏之外。完整说明留在 title 里。
                */}
                <div className="mt-auto flex items-center gap-1.5">
                  {item.detail && (
                    <span className="min-w-0 flex-1 truncate text-[11px] leading-tight text-muted-foreground" title={item.detail}>
                      {item.detail}
                    </span>
                  )}
                  {item.action && (
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="h-5 shrink-0 px-1.5 text-[10px]"
                      data-testid={`runtime-readiness-action-${item.key}`}
                      onClick={() => onAction(item.action!.key)}
                    >
                      {item.action.label}
                    </Button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}
