import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 页面壳（FR-502 阶段 3）：统一所有内容页的留白、纵向节奏与滚动模型。
 *
 * 依据《资源优先工作区》原型实测的三种壳态：
 *   default — 整页滚动（原型 `.page`）
 *   fixed   — 固定视口、内部区域自行滚动（原型 `.page.fixed`）；用于列表页与设置页，
 *             使工具栏/表头常驻、只有数据区滚动
 *   tool    — 全占满、无外层留白（原型 `.page.tool`）；用于终端/文件等自带滚动的工具页
 *
 * 规范第一条：任何内容页的第一个子元素都应为 `PageHeader`。
 */
export function PageShell({
  variant = 'default',
  className,
  children,
  ...props
}: React.ComponentProps<'div'> & {
  /** 滚动模型；默认整页滚动。 */
  variant?: 'default' | 'fixed' | 'tool'
}) {
  return (
    <div
      data-slot="page-shell"
      data-variant={variant}
      className={cn(
        /*
          【为什么是「近左上加轻内边距 + 随视口伸缩」】此前写死 `px-[25px] py-[22px]`：
          在 1280 宽下边距占比尚可，但在 1920 上显得内容被挤在中间一块窄条里，
          而在 1024 上又偏紧。改成 clamp 后，小屏收到 16px（内容优先）、大屏最多 28px
          （留白更从容），中间连续过渡——它同时满足「内容铺满」与「不贴边」两个要求。

          `ps-*`（padding-inline-start）而不是 `px-*`：用户要求**左对齐**，
          左右边距可以不对称——左侧紧跟侧栏（窄），右侧多留一点呼吸（宽）。
        */
        'flex min-h-0 flex-1 flex-col gap-[12px]',
        // 用户要求「更靠近左上方」，上/左已收到近乎贴边的程度（上 4-10 / 左 6-14）；
        // 右侧保留更宽的呼吸（内容以左对齐为主，右侧多留白不显局促）。
        'ps-[clamp(6px,0.9vw,14px)] pe-[clamp(16px,2vw,36px)]',
        'pt-[clamp(4px,0.6vh,10px)] pb-[clamp(14px,1.8vh,24px)]',
        // `w-full` + `max-w-none`：铺满内容区，不做居中收窄（用户要求铺满整页）。
        'w-full min-w-0',
        variant === 'default' && 'overflow-auto',
        variant === 'fixed' && 'overflow-hidden',
        variant === 'tool' && 'gap-0 p-0',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}
