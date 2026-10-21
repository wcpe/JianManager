import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { Alert, AlertDescription, AlertTitle } from "./alert"
import { Avatar, AvatarFallback } from "./avatar"
import { Kbd } from "./kbd"
import { Progress } from "./progress"
import { Separator } from "./separator"

/**
 * 展示类原语契约（上游拉取适配）：Kbd / Alert / Avatar / Progress / Separator。
 * 只锁对外可见的关键语义与 data-slot 钩子，不测视觉。
 */
describe("展示类原语", () => {
  it("Kbd 渲染 kbd 元素与 data-slot", () => {
    render(<Kbd data-testid="kbd">⌘K</Kbd>)
    const kbd = screen.getByTestId("kbd")
    expect(kbd.tagName).toBe("KBD")
    expect(kbd).toHaveAttribute("data-slot", "kbd")
  })

  it("Alert 以 role=alert 呈现并可带标题/描述", () => {
    render(
      <Alert data-testid="alert">
        <AlertTitle>注意</AlertTitle>
        <AlertDescription>磁盘接近上限</AlertDescription>
      </Alert>,
    )
    const alert = screen.getByRole("alert")
    expect(alert).toHaveAttribute("data-slot", "alert")
    expect(alert).toHaveTextContent("磁盘接近上限")
    expect(screen.getByText("注意")).toHaveAttribute("data-slot", "alert-title")
  })

  it("Avatar 回落文本与尺寸钩子", () => {
    render(
      <Avatar size="sm">
        <AvatarFallback>AB</AvatarFallback>
      </Avatar>,
    )
    const avatar = document.querySelector('[data-slot="avatar"]')
    expect(avatar).toHaveAttribute("data-size", "sm")
    expect(screen.getByText("AB")).toHaveAttribute("data-slot", "avatar-fallback")
  })

  it("Progress 暴露进度语义", () => {
    render(<Progress value={40} data-testid="progress" />)
    const progress = screen.getByTestId("progress")
    expect(progress).toHaveAttribute("data-slot", "progress")
    expect(progress).toHaveAttribute("aria-valuenow", "40")
  })

  it("Separator 方向钩子与装饰语义", () => {
    render(<Separator data-testid="sep" orientation="vertical" />)
    const sep = screen.getByTestId("sep")
    expect(sep).toHaveAttribute("data-slot", "separator")
    expect(sep).toHaveAttribute("data-orientation", "vertical")
  })
})
