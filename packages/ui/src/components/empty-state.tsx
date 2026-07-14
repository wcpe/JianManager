/**
 * 空态原语。
 *
 * 统一「暂无数据 / 搜索无结果 / 未配置」等空态：图标 + 标题 + 可选说明 + 可选操作引导，
 * 替代各页内联 `<p>`，给用户明确下一步。走主题 CSS 变量，暗/亮色自适应。
 */
import * as React from 'react'
import { cn } from '../lib/utils'

interface EmptyStateProps {
  /** 顶部图标（lucide 组件），可选。 */
  icon?: React.ReactNode
  /** 标题（已翻译文案）。 */
  title: string
  /** 说明，指引下一步，可选。 */
  description?: string
  /** 操作引导（按钮/链接），可选。 */
  action?: React.ReactNode
  className?: string
}

/** 居中空态块：图标弱化、标题为主、说明与操作按需。 */
export function EmptyState({ icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div
      data-slot="empty-state"
      className={cn(
        'flex flex-col items-center justify-center gap-2 px-6 py-10 text-center',
        className,
      )}
    >
      {icon && (
        <div className="text-muted-foreground [&_svg]:size-8" aria-hidden>
          {icon}
        </div>
      )}
      <p className="text-sm font-medium text-foreground">{title}</p>
      {description && <p className="max-w-sm text-xs text-muted-foreground">{description}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}
