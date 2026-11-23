import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "./accordion"

/**
 * Accordion 契约（上游拉取适配）：单开模式下点开一项展开其内容、触发器 data-state=open；
 * 结构与 data-slot 钩子齐全（样式/测试定位）。
 */
describe("Accordion", () => {
  it("点击触发器展开对应内容", async () => {
    const user = userEvent.setup()
    render(
      <Accordion type="single" collapsible>
        <AccordionItem value="a">
          <AccordionTrigger>第一节</AccordionTrigger>
          <AccordionContent>第一节内容</AccordionContent>
        </AccordionItem>
        <AccordionItem value="b">
          <AccordionTrigger>第二节</AccordionTrigger>
          <AccordionContent>第二节内容</AccordionContent>
        </AccordionItem>
      </Accordion>,
    )

    const trigger = screen.getByRole("button", { name: /第一节/ })
    expect(trigger).toHaveAttribute("data-slot", "accordion-trigger")
    expect(screen.queryByText("第一节内容")).toBeNull()

    await user.click(trigger)
    expect(trigger).toHaveAttribute("data-state", "open")
    expect(screen.getByText("第一节内容")).toBeInTheDocument()
  })
})
