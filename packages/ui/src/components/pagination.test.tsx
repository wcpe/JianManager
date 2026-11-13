import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
} from "./pagination"

/**
 * Pagination 契约（上游拉取适配）：链接语义（`<a>` + Button asChild）；
 * 当前页以 aria-current="page" 标注；前后页带可访问名。
 */
describe("Pagination", () => {
  it("渲染导航、当前页语义与前后页可访问名", () => {
    render(
      <Pagination>
        <PaginationContent>
          <PaginationItem>
            <PaginationPrevious href="?page=0" />
          </PaginationItem>
          <PaginationItem>
            <PaginationLink href="?page=1" isActive>
              1
            </PaginationLink>
          </PaginationItem>
          <PaginationItem>
            <PaginationLink href="?page=2">2</PaginationLink>
          </PaginationItem>
          <PaginationItem>
            <PaginationNext href="?page=2" />
          </PaginationItem>
        </PaginationContent>
      </Pagination>,
    )

    expect(screen.getByRole("navigation", { name: "pagination" })).toHaveAttribute(
      "data-slot",
      "pagination",
    )
    const current = screen.getByRole("link", { name: "1" })
    expect(current).toHaveAttribute("aria-current", "page")
    expect(current).toHaveAttribute("data-active", "true")
    expect(screen.getByRole("link", { name: "Go to previous page" })).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Go to next page" })).toBeInTheDocument()
  })
})
