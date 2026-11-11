import * as React from 'react'
import { HelpCircleIcon } from 'lucide-react'

import { cn } from '../../lib/utils'
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from '../popover'

/**
 * 页面帮助浮层（点击问号展开）。
 *
 * # 为何是 Popover 而不是 Tooltip
 *
 * Tooltip 只承载**单行纯文本**（见 tooltip.tsx 约定）。页面帮助天然是「分节标题 +
 * 若干条目」的结构化内容；用 Tooltip 塞进去既在触摸设备上无法展开，也无法阅读长段落。
 * `PageHeader` 的 `description` 走 Tooltip，只适合一句话；需要讲清楚时用本组件。
 *
 * # 为何放在 packages/ui 而不是各应用里
 *
 * 这套「问号 + 分节说明」先在日志中心验证好用，接下来要在多个页面铺开。
 * 各应用各存一份等于多份需要同步维护的交互细节（键盘可达性、aria 标签、
 * Popover 层级），迟早不一致。
 */
export interface PageHelpSection {
  /** 小节标题。 */
  title: React.ReactNode
  /** 段落正文（可选）。 */
  body?: React.ReactNode
  /** 条目列表（可选）。两者都给时先段落后列表。 */
  items?: React.ReactNode[]
}

export function PageHelp({
  title,
  intro,
  sections,
  testId = 'page-help',
  contentClassName,
}: {
  /** 浮层标题，同时作为按钮的无障碍名。 */
  title: string
  /** 标题下的一句话导语。 */
  intro?: React.ReactNode
  sections: PageHelpSection[]
  /** 测试前缀：触发器 `${testId}-trigger`，内容 `${testId}-content`。 */
  testId?: string
  contentClassName?: string
}) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          data-testid={`${testId}-trigger`}
          data-slot="page-help-trigger"
          aria-label={title}
          className="inline-flex size-5 shrink-0 items-center justify-center rounded-full text-muted-foreground/70 transition-colors hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:outline-none"
        >
          <HelpCircleIcon className="size-3.5" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className={cn('w-96 max-h-[70vh] overflow-y-auto', contentClassName)}
        data-testid={`${testId}-content`}
      >
        <PopoverHeader>
          <PopoverTitle>{title}</PopoverTitle>
          {intro ? <PopoverDescription>{intro}</PopoverDescription> : null}
        </PopoverHeader>
        <div className="mt-3 flex flex-col gap-3 text-xs leading-relaxed">
          {sections.map((section, index) => (
            <section key={index} data-testid={`${testId}-section-${index}`}>
              <p className="mb-1 font-medium">{section.title}</p>
              {section.body ? <p className="text-muted-foreground">{section.body}</p> : null}
              {section.items && section.items.length > 0 ? (
                <ul className="flex list-disc flex-col gap-1 pl-4 text-muted-foreground">
                  {section.items.map((item, i) => (
                    <li key={i}>{item}</li>
                  ))}
                </ul>
              ) : null}
            </section>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  )
}
