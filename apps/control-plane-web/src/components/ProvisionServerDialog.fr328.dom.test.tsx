import { describe, it, expect, beforeAll } from 'vitest'
import { useState } from 'react'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import ProvisionServerDialog from './ProvisionServerDialog'

/**
 * FR-328：一键搭建后端子服 MC 版本下拉长列表可滚动。
 *
 * 根因（真机 Chrome 实证）：对话框是模态 Radix Dialog（react-remove-scroll 滚动锁），
 * Combobox 下拉 portal 到 body、落在滚动锁的「模态外区域」，滚轮/触摸滚动事件被
 * preventDefault 吞掉——列表本身的 max-h + overflow-y-auto 结构无病，程序化 scrollTop 正常。
 * 修复：Combobox 的 Popover 置 modal，自己注册滚动锁分片（body[data-scroll-locked] 1→2），
 * 滚轮事件放行（修前 wheel defaultPrevented=true → 修后 false）。
 *
 * jsdom 无真实滚动，按机制断言：① 列表滚动结构（视口约束 + 内滚）仍在；
 * ② 打开下拉后滚动锁计数 +1（= Popover 自持滚动锁，模态 Dialog 的锁不再吞下拉滚动）。
 * 真浏览器长列表滚动手感 → 待真机验。
 */

// Radix（Popover/Dialog）在 jsdom 下依赖以下 API；vitest jsdom 默认缺，按标准配方补齐。
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

/** 长版本列表（如 Paper 全版本 60+）：覆盖 seed 的 4 条，逼近真机形态。 */
const LONG_VERSIONS = Array.from({ length: 60 }, (_, i) => `1.${i}.0`)

function useLongVersionList() {
  server.use(
    http.get(API('/cores'), ({ request }) => {
      const url = new URL(request.url)
      // 仅覆盖版本列表分支；解析分支（带 mcVersion）返回 undefined 落回 seed handler。
      if (url.searchParams.get('mcVersion')) return undefined
      return HttpResponse.json({ type: url.searchParams.get('type') ?? 'paper', versions: LONG_VERSIONS })
    }),
  )
}

function Harness() {
  const [open, setOpen] = useState(true)
  return <ProvisionServerDialog open={open} onClose={() => setOpen(false)} />
}

describe('ProvisionServerDialog MC 版本下拉可滚动（FR-328）', () => {
  it('长版本列表渲染进有视口约束的内滚容器（max-h + overflow-y-auto）', async () => {
    const user = userEvent.setup()
    loginMockUser()
    useLongVersionList()
    renderWithProviders(<Harness />)

    expect(await screen.findByRole('dialog', { name: '一键搭建后端子服' })).toBeInTheDocument()
    // 触发器占位在版本列表加载完成后才从「加载版本中…」变为「选择版本」
    await user.click(await screen.findByText('选择版本'))

    // 60 条版本全部渲染在列表容器内
    expect(await screen.findByRole('button', { name: '1.0.0' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '1.59.0' })).toBeInTheDocument()

    const list = document.querySelector('[data-slot="combobox-list"]') as HTMLElement
    expect(list).toBeTruthy()
    expect(list.className).toContain('max-h-56')
    expect(list.className).toContain('overflow-y-auto')
    expect(list.contains(screen.getByRole('button', { name: '1.59.0' }))).toBe(true)
  })

  it('下拉在模态对话框内打开时自持滚动锁（body data-scroll-locked 1→2），滚轮不再被模态锁吞', async () => {
    const user = userEvent.setup()
    loginMockUser()
    useLongVersionList()
    renderWithProviders(<Harness />)

    expect(await screen.findByRole('dialog', { name: '一键搭建后端子服' })).toBeInTheDocument()
    // 模态 Dialog 自身的滚动锁
    await waitFor(() => expect(document.body.getAttribute('data-scroll-locked')).toBe('1'))

    await user.click(await screen.findByText('选择版本'))
    expect(await screen.findByRole('button', { name: '1.0.0' })).toBeInTheDocument()
    // Popover modal：下拉注册自己的滚动锁 → 计数 +1。若退化为非 modal（回归 FR-328），
    // 计数停在 1，portal 到 body 的下拉滚动会被 Dialog 的锁 preventDefault 吞掉。
    await waitFor(() => expect(document.body.getAttribute('data-scroll-locked')).toBe('2'))

    // 选中后下拉关闭，锁计数回落，Dialog 的锁不受影响
    await user.click(screen.getByRole('button', { name: '1.0.0' }))
    await waitFor(() => expect(document.body.getAttribute('data-scroll-locked')).toBe('1'))
    expect(screen.getByRole('dialog', { name: '一键搭建后端子服' })).toBeInTheDocument()
  })

  it('节点 / JDK / 用户组下拉同为共享 Combobox，同样自持滚动锁', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<Harness />)

    expect(await screen.findByRole('dialog', { name: '一键搭建后端子服' })).toBeInTheDocument()
    await user.click(screen.getByText('选择节点'))
    expect(await screen.findByRole('button', { name: 'alpha' })).toBeInTheDocument()
    await waitFor(() => expect(document.body.getAttribute('data-scroll-locked')).toBe('2'))
  })
})
