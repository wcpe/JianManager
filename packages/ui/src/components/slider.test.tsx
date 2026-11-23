import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { Slider } from "./slider"

/**
 * Slider 契约（上游拉取适配）：role=slider 暴露值语义；键盘步进改值；
 * 多拇指（数组 value）按个数渲染。
 */
describe("Slider", () => {
  it("键盘步进更新 aria-valuenow", async () => {
    const user = userEvent.setup()
    render(<Slider aria-label="内存上限" defaultValue={[40]} />)

    const slider = screen.getByRole("slider", { name: "内存上限" })
    expect(slider).toHaveAttribute("aria-valuenow", "40")

    slider.focus()
    await user.keyboard("{ArrowRight}")
    expect(slider).toHaveAttribute("aria-valuenow", "41")
  })

  it("数组默认值渲染多个拇指", () => {
    render(<Slider aria-label="区间" defaultValue={[20, 80]} />)
    expect(screen.getAllByRole("slider")).toHaveLength(2)
  })
})
