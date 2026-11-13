import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { RadioGroup, RadioGroupItem } from "./radio-group"

/**
 * RadioGroup 契约（上游拉取适配）：role=radiogroup + radio；点选切换 aria-checked；
 * data-slot 钩子供样式/测试定位。
 */
describe("RadioGroup", () => {
  it("点选切换选中态", async () => {
    const user = userEvent.setup()
    render(
      <RadioGroup defaultValue="a" aria-label="部署方式">
        <RadioGroupItem value="a" aria-label="就地" />
        <RadioGroupItem value="b" aria-label="迁移" />
      </RadioGroup>,
    )

    const group = screen.getByRole("radiogroup", { name: "部署方式" })
    expect(group).toHaveAttribute("data-slot", "radio-group")

    const inPlace = screen.getByRole("radio", { name: "就地" })
    const migrate = screen.getByRole("radio", { name: "迁移" })
    expect(inPlace).toHaveAttribute("aria-checked", "true")

    await user.click(migrate)
    expect(migrate).toHaveAttribute("aria-checked", "true")
    expect(inPlace).toHaveAttribute("aria-checked", "false")
  })
})
