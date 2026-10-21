import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "./tooltip"

/**
 * Tooltip 契约（上游拉取适配）：默认关闭不挂 DOM；`defaultOpen` 时内容以 role=tooltip 呈现，
 * 带 data-slot 钩子；Provider 可局部包裹（本项目未要求全局 Provider）。
 */
describe("Tooltip", () => {
  it("默认关闭：内容不挂 DOM", () => {
    render(
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger>悬停</TooltipTrigger>
          <TooltipContent>提示</TooltipContent>
        </Tooltip>
      </TooltipProvider>,
    )

    expect(screen.queryByRole("tooltip")).toBeNull()
  })

  it("defaultOpen：可见内容与可访问的 tooltip 语义都呈现", () => {
    render(
      <TooltipProvider>
        <Tooltip defaultOpen>
          <TooltipTrigger>悬停</TooltipTrigger>
          <TooltipContent>提示文案</TooltipContent>
        </Tooltip>
      </TooltipProvider>,
    )

    // 可见内容按 data-slot 断言（Radix 1.2.x 起，role="tooltip" 不再落在可见内容上，
    // 而是放在「供 aria-describedby 的隐藏副本」——两者分开断言，不依赖元素同一性）。
    const visible = document.querySelector('[data-slot="tooltip-content"]')
    expect(visible).not.toBeNull()
    expect(visible).toHaveTextContent("提示文案")
    expect(screen.getByRole("tooltip")).toBeInTheDocument()
  })
})
