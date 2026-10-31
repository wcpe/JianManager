import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "./popover"

/**
 * Popover 契约（上游拉取适配）：默认关闭不挂 DOM；`defaultOpen` 时内容挂载且带 data-slot 钩子；
 * Header / Title / Description 槽位齐全（说明类浮层的标准结构）。
 */
describe("Popover", () => {
  it("默认关闭：内容不挂 DOM", () => {
    render(
      <Popover>
        <PopoverTrigger>打开</PopoverTrigger>
        <PopoverContent>面板</PopoverContent>
      </Popover>,
    )

    expect(screen.queryByText("面板")).toBeNull()
  })

  it("defaultOpen：内容与结构槽位呈现", () => {
    render(
      <Popover defaultOpen>
        <PopoverTrigger>打开</PopoverTrigger>
        <PopoverContent>
          <PopoverHeader>
            <PopoverTitle>标题</PopoverTitle>
            <PopoverDescription>描述</PopoverDescription>
          </PopoverHeader>
        </PopoverContent>
      </Popover>,
    )

    const content = screen.getByText("标题").closest('[data-slot="popover-content"]')
    expect(content).not.toBeNull()
    expect(content).toHaveAttribute("data-slot", "popover-content")
    expect(screen.getByText("描述")).toHaveAttribute("data-slot", "popover-description")
  })
})
