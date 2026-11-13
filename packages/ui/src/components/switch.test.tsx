import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { Switch } from "./switch"

/**
 * Switch 契约（上游拉取适配）：role=switch 承载可访问名（护栏：名字落在可聚焦元素上）；
 * 点击切换 data-state / aria-checked；size 走 data-size 钩子；禁用态不触发回调。
 */
describe("Switch", () => {
  it("点击切换状态并触发回调", async () => {
    const user = userEvent.setup()
    const onCheckedChange = vi.fn()
    render(<Switch aria-label="启用探针" onCheckedChange={onCheckedChange} />)

    const sw = screen.getByRole("switch", { name: "启用探针" })
    expect(sw).toHaveAttribute("data-state", "unchecked")
    expect(sw).toHaveAttribute("data-size", "default")

    await user.click(sw)
    expect(onCheckedChange).toHaveBeenCalledWith(true)
    expect(sw).toHaveAttribute("data-state", "checked")
    expect(sw).toHaveAttribute("aria-checked", "true")
  })

  it("size=sm 透传到 data-size", () => {
    render(<Switch aria-label="小开关" size="sm" />)
    expect(screen.getByRole("switch", { name: "小开关" })).toHaveAttribute("data-size", "sm")
  })

  it("禁用态不触发回调", async () => {
    const user = userEvent.setup()
    const onCheckedChange = vi.fn()
    render(<Switch aria-label="禁用开关" disabled onCheckedChange={onCheckedChange} />)

    await user.click(screen.getByRole("switch", { name: "禁用开关" }))
    expect(onCheckedChange).not.toHaveBeenCalled()
  })
})
