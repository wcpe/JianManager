import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "./collapsible"

/**
 * Collapsible 契约（上游拉取适配）：关闭时内容不挂 DOM；点击触发器展开/收起；
 * data-state 钩子随开合变化。
 */
describe("Collapsible", () => {
  it("点击触发器开合内容", async () => {
    const user = userEvent.setup()
    render(
      <Collapsible>
        <CollapsibleTrigger>展开</CollapsibleTrigger>
        <CollapsibleContent>内容区</CollapsibleContent>
      </Collapsible>,
    )

    const trigger = screen.getByRole("button", { name: "展开" })
    expect(trigger).toHaveAttribute("data-state", "closed")
    expect(screen.queryByText("内容区")).toBeNull()

    await user.click(trigger)
    expect(trigger).toHaveAttribute("data-state", "open")
    expect(screen.getByText("内容区")).toBeInTheDocument()
  })
})
