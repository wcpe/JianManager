import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { Toggle } from "./toggle"

/**
 * Toggle 契约（上游拉取适配）：role=button + aria-pressed 表达持久开关态；
 * 点击切换 data-state（on/off）；变体/尺寸走 data 钩子。
 */
describe("Toggle", () => {
  it("点击切换 aria-pressed 与 data-state", async () => {
    const user = userEvent.setup()
    render(<Toggle aria-label="加粗">B</Toggle>)

    const toggle = screen.getByRole("button", { name: "加粗" })
    expect(toggle).toHaveAttribute("aria-pressed", "false")
    expect(toggle).toHaveAttribute("data-state", "off")

    await user.click(toggle)
    expect(toggle).toHaveAttribute("aria-pressed", "true")
    expect(toggle).toHaveAttribute("data-state", "on")
  })
})
