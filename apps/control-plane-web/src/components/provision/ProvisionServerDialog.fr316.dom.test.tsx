import { describe, it, expect, beforeAll } from 'vitest'
import { useState } from 'react'
import { http, HttpResponse } from 'msw'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import ProvisionServerDialog from './ProvisionServerDialog'

/**
 * ProvisionServerDialog FR-316 强断言：版本-JDK 兼容预检。
 * 解析响应（GET /cores 带 mcVersion）携带 javaMajorRequired，向导据此校验所选/默认 JDK：
 * ① 选 26.1（需 Java 25）而节点最高 JDK 21 → 表单级阻断（提交禁用 + 差距文案 + 去 JDK 面板链接）；
 * ② 选 1.21.1（需 Java 21）且默认 JDK 21 → 放行（无阻断文案、提交可点）；
 * ③ 节点无任何 JDK → 阻断 + 引导安装。
 * 独立于既有 ProvisionServerDialog.dom.test.tsx（不动其用例）。
 */

// Radix（Popover/Select/Dialog）在 jsdom 下依赖以下 API；vitest jsdom 默认缺，按标准配方补齐。
beforeAll(() => {
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

function Harness() {
  const [open, setOpen] = useState(true)
  return <ProvisionServerDialog open={open} onClose={() => setOpen(false)} />
}

/** 通过 Combobox 选项选择一个已知值：点触发器（按当前显示文案）→ 点选项。 */
async function pickCombo(
  user: ReturnType<typeof userEvent.setup>,
  triggerText: string,
  optionText: string,
) {
  await user.click(within(document.body as HTMLElement).getByText(triggerText))
  const option = await screen.findByRole('button', { name: optionText })
  await user.click(option)
}

/** 填齐名称/节点/版本三个必填项，让 JDK 预检成为唯一潜在阻断因素。 */
async function fillRequired(user: ReturnType<typeof userEvent.setup>, version: string) {
  await user.type(screen.getByPlaceholderText('lobby'), 'jdk-precheck')
  await pickCombo(user, '选择节点', 'alpha')
  await pickCombo(user, '选择版本', version)
}

describe('ProvisionServerDialog FR-316（版本-JDK 兼容预检）', () => {
  it('① 选 26.1（需 Java 25）而节点最高 JDK 21 → 阻断 + 差距文案 + 引导链接', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<Harness />)

    await screen.findByRole('dialog', { name: '一键搭建后端子服' })
    await fillRequired(user, '26.1')

    // 差距文案：指明需要的 Java 版本与当前所选/默认 JDK（seed 最高 temurin 21 自动选中）的差距。
    const blocked = await screen.findByText(/MC 26\.1 需要 Java 25\+/)
    expect(blocked.textContent).toContain('Java 21')
    // 表单级阻断：提交按钮禁用。
    expect(screen.getByRole('button', { name: '搭建' })).toBeDisabled()
    // 引导去 JDK 面板安装。
    const link = screen.getByRole('link', { name: '去 JDK 面板安装' })
    expect(link).toHaveAttribute('href', '/runtime-assets')
  })

  it('② 选 1.21.1（需 Java 21）且默认 JDK 21 满足 → 放行', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<Harness />)

    await screen.findByRole('dialog', { name: '一键搭建后端子服' })
    await fillRequired(user, '1.21.1')

    // 等解析预览到位（javaMajorRequired 已返回），再断言无阻断。
    await screen.findByText(/将下载/)
    expect(screen.queryByText(/需要 Java/)).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: '去 JDK 面板安装' })).not.toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('button', { name: '搭建' })).toBeEnabled())
  })

  it('③ 节点无任何 JDK → 阻断并引导安装', async () => {
    const user = userEvent.setup()
    loginMockUser()
    server.use(http.get(API('/nodes/:id/jdks'), () => HttpResponse.json([])))
    renderWithProviders(<Harness />)

    await screen.findByRole('dialog', { name: '一键搭建后端子服' })
    await fillRequired(user, '1.21.1')

    await screen.findByText(/未安装任何 JDK/)
    expect(screen.getByText(/需要 Java 21\+/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '搭建' })).toBeDisabled()
    expect(screen.getByRole('link', { name: '去 JDK 面板安装' })).toHaveAttribute('href', '/runtime-assets')
  })
})
