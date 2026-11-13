import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import {
  ContextMenu,
  ContextMenuCheckboxItem,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuShortcut,
} from "./context-menu"

/**
 * ContextMenu 契约（2026-10 上游同步新增零件）：受控 open 下渲染内容；
 * CheckboxItem/Shortcut 的 data-slot 钩子与角色语义。
 */
describe("ContextMenu（同步新增零件）", () => {
  it("受控打开渲染内容与新增零件", () => {
    render(
      <ContextMenu open>
        <div>触发区</div>
        <ContextMenuContent>
          <ContextMenuCheckboxItem checked onCheckedChange={() => {}}>
            显示隐藏文件
          </ContextMenuCheckboxItem>
          <ContextMenuSeparator data-testid="sep" />
          <ContextMenuItem>
            重命名 <ContextMenuShortcut>F2</ContextMenuShortcut>
          </ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>,
    )

    expect(screen.getByRole("menuitemcheckbox", { name: "显示隐藏文件" })).toHaveAttribute(
      "data-slot",
      "context-menu-checkbox-item",
    )
    expect(screen.getByText("F2")).toHaveAttribute("data-slot", "context-menu-shortcut")
    expect(screen.getByTestId("sep")).toHaveAttribute("data-slot", "context-menu-separator")
  })
})
