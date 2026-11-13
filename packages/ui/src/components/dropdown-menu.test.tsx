import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "./dropdown-menu"

/**
 * DropdownMenu 契约（2026-10 上游同步新增零件）：CheckboxItem 勾选回调、
 * Sub 子菜单结构、Radio/Shortcut/Group 的 data-slot 钩子。
 * 打开态用受控 open（jsdom 无指针，避免依赖悬停/点击打开）。
 */
describe("DropdownMenu（同步新增零件）", () => {
  it("CheckboxItem 点击触发 onCheckedChange 并带指示器钩子", async () => {
    const user = userEvent.setup()
    const onCheckedChange = vi.fn()
    render(
      <DropdownMenu open>
        <DropdownMenuTrigger>打开</DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuCheckboxItem checked={false} onCheckedChange={onCheckedChange}>
            仅运行中
          </DropdownMenuCheckboxItem>
        </DropdownMenuContent>
      </DropdownMenu>,
    )

    const item = screen.getByRole("menuitemcheckbox", { name: "仅运行中" })
    expect(item).toHaveAttribute("data-slot", "dropdown-menu-checkbox-item")
    await user.click(item)
    expect(onCheckedChange).toHaveBeenCalledWith(true)
  })

  it("Sub 子菜单结构完整（触发器带箭头，内容有独立钩子）", () => {
    render(
      <DropdownMenu open>
        <DropdownMenuTrigger>打开</DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuGroup data-testid="group">
            <DropdownMenuLabel>分组标题</DropdownMenuLabel>
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>更多操作</DropdownMenuSubTrigger>
              <DropdownMenuSubContent>子项</DropdownMenuSubContent>
            </DropdownMenuSub>
            <DropdownMenuSeparator data-testid="sep" />
            <DropdownMenuItem>
              导出 <DropdownMenuShortcut>⌘E</DropdownMenuShortcut>
            </DropdownMenuItem>
            <DropdownMenuRadioGroup>
              <DropdownMenuRadioItem value="a">按节点</DropdownMenuRadioItem>
            </DropdownMenuRadioGroup>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>,
    )

    expect(screen.getByTestId("group")).toHaveAttribute("data-slot", "dropdown-menu-group")
    const subTrigger = screen.getByRole("menuitem", { name: "更多操作" })
    expect(subTrigger).toHaveAttribute("data-slot", "dropdown-menu-sub-trigger")
    expect(screen.getByText("⌘E")).toHaveAttribute("data-slot", "dropdown-menu-shortcut")
    expect(screen.getByRole("menuitemradio", { name: "按节点" })).toHaveAttribute(
      "data-slot",
      "dropdown-menu-radio-item",
    )
  })
})
