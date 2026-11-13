import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { ToggleGroup, ToggleGroupItem } from "./toggle-group"

/**
 * ToggleGroup 契约（上游拉取适配）：single 组互斥切换（点击项 data-state=on，其余回 off）；
 * 组容器携带 data-spacing / data-orientation 钩子。
 */
describe("ToggleGroup", () => {
  it("single 组内互斥选中", async () => {
    const user = userEvent.setup()
    render(
      <ToggleGroup type="single" aria-label="视图">
        <ToggleGroupItem value="table" aria-label="表格">
          表
        </ToggleGroupItem>
        <ToggleGroupItem value="card" aria-label="卡片">
          卡
        </ToggleGroupItem>
      </ToggleGroup>,
    )

    const group = screen.getByRole("radiogroup", { name: "视图" })
    expect(group).toHaveAttribute("data-spacing", "2")
    expect(group).toHaveAttribute("data-orientation", "horizontal")

    const table = screen.getByRole("radio", { name: "表格" })
    const card = screen.getByRole("radio", { name: "卡片" })

    await user.click(table)
    expect(table).toHaveAttribute("data-state", "on")

    await user.click(card)
    expect(card).toHaveAttribute("data-state", "on")
    expect(table).toHaveAttribute("data-state", "off")
  })
})
