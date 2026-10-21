import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "./sheet"

/**
 * Sheet 契约（2026-10 上游同步）：四向 side 透传 data-side；Header/Footer/Title/Description
 * 槽位齐全；关闭钮为可聚焦按钮且带可访问名（sr-only，默认英文回落）。
 */
describe("Sheet（上游同步）", () => {
  it("打开态：内容带 data-side 与槽位，关闭钮可访问", () => {
    render(
      <Sheet open>
        <SheetContent side="right">
          <SheetHeader>
            <SheetTitle>配置版本</SheetTitle>
            <SheetDescription>历史版本对比</SheetDescription>
          </SheetHeader>
          <SheetFooter data-testid="footer">页脚</SheetFooter>
        </SheetContent>
      </Sheet>,
    )

    const content = document.querySelector('[data-slot="sheet-content"]')
    expect(content).toHaveAttribute("data-side", "right")
    expect(screen.getByText("配置版本")).toHaveAttribute("data-slot", "sheet-title")
    expect(screen.getByTestId("footer")).toHaveAttribute("data-slot", "sheet-footer")
    // 关闭钮：可聚焦元素（button）承载可访问名（护栏：名字落在可聚焦元素上）。
    expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument()
  })

  it("showCloseButton=false 时不渲染关闭钮", () => {
    render(
      <Sheet open>
        <SheetContent showCloseButton={false}>
          <SheetTitle>无关闭钮</SheetTitle>
        </SheetContent>
      </Sheet>,
    )
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull()
  })
})
