import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { NativeSelect, NativeSelectOption } from "./native-select"

/**
 * NativeSelect 契约（上游拉取适配）：原生 select 语义（role=combobox）；
 * 选择经原生事件生效；size 走 data 钩子；箭头图标装饰性（aria-hidden）。
 */
describe("NativeSelect", () => {
  it("选择选项生效并携带 data 钩子", async () => {
    const user = userEvent.setup()
    render(
      <NativeSelect aria-label="部署方式" size="sm" defaultValue="a">
        <NativeSelectOption value="a">就地</NativeSelectOption>
        <NativeSelectOption value="b">迁移</NativeSelectOption>
      </NativeSelect>,
    )

    const select = screen.getByRole("combobox", { name: "部署方式" })
    expect(select).toHaveAttribute("data-size", "sm")
    expect(select).toHaveValue("a")

    await user.selectOptions(select, "b")
    expect(select).toHaveValue("b")

    const icon = document.querySelector('[data-slot="native-select-icon"]')
    expect(icon).toHaveAttribute("aria-hidden", "true")
  })
})
