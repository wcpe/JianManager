import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import '@/i18n'
import { NodeInstallSection } from './NodeInstallSection'

/**
 * Node.js 安装段的行为。
 *
 * 【这组用例从 NodeRuntimeSection 迁移而来】安装能力原先是嵌在运行时列表视图里的
 * 一个模态（那个视图同时管列表、扫描、删除）。用户报的「点安装 Node.js 打开的是 JDK」
 * 就源于这种堆叠；现在安装是独立视图，测试对象随之迁到这里。
 * 迁移时保留了原先的全部行为断言，没有放宽。
 */

const renderSection = (over: { installing?: boolean; onInstall?: (m: number) => Promise<boolean> } = {}) => {
  const onInstall = over.onInstall ?? vi.fn(async () => true)
  render(<NodeInstallSection installing={over.installing ?? false} onInstall={onInstall} />)
  return { onInstall }
}

describe('NodeInstallSection', () => {
  it('版本用下拉选择，预选当前推荐版本并展示 LTS 与维护截止', async () => {
    renderSection()
    // 不手输：输入框（Combobox 触发器）里已经是预选好的完整描述。
    const combo = await screen.findByRole('button', { name: /Node\.js 版本|选择版本/ })
    expect(combo).toHaveTextContent('Node.js 22 · LTS Jod · 当前推荐 · 维护至 2027-04')
  })

  it('选 20 后提交上报 20（不再依赖自由输入）', async () => {
    const user = userEvent.setup()
    const { onInstall } = renderSection()

    const combo = await screen.findByRole('button', { name: /Node\.js 版本|选择版本/ })
    await user.click(combo)
    await user.click(await screen.findByText(/Node\.js 20 · LTS Iron/))

    await user.click(screen.getByTestId('node-install-submit'))
    await waitFor(() => expect(onInstall).toHaveBeenCalledWith(20))
  })

  it('安装预览随所选版本更新（提交前就知道将装什么）', async () => {
    const user = userEvent.setup()
    renderSection()
    // 默认 22 → 预览写明 LTS 代号。
    expect(await screen.findByText(/将安装 Node\.js 22（LTS Jod）/)).toBeInTheDocument()

    const combo = await screen.findByRole('button', { name: /Node\.js 版本|选择版本/ })
    await user.click(combo)
    await user.click(await screen.findByText(/Node\.js 24 · Current/))
    expect(await screen.findByText(/将安装 Node\.js 24（Current）/)).toBeInTheDocument()
  })

  it('在途时按钮禁用且文案切换（用户能看出正在下发）', async () => {
    /*
      【能证明什么、不能证明什么】
      能证明：提交在途时按钮禁用、文案变「正在下发…」。
      不能证明：它能挡住同一渲染周期内的两次点击——jsdom + RTL 下即使 fireEvent 同步
      连发，React 也会在两次之间刷新状态，setState 版防重在这里同样看不出问题。
      闸门的区分性由 use-submit-guard.dom.test.tsx 在钩子层保证。
    */
    let resolveInstall: (v: boolean) => void = () => {}
    const onInstall = vi.fn(
      () =>
        new Promise<boolean>((res) => {
          resolveInstall = res
        }),
    )
    renderSection({ onInstall })

    const submit = await screen.findByTestId('node-install-submit')
    await act(async () => {
      fireEvent.click(submit)
    })

    expect(onInstall).toHaveBeenCalledTimes(1)
    expect(submit).toBeDisabled()
    expect(submit).toHaveTextContent('正在下发…')

    await act(async () => {
      resolveInstall(true)
    })
    await waitFor(() => expect(screen.getByTestId('node-install-submit')).toBeEnabled())
  })

  it('外壳 mutation 在途时也禁用（不只挡本组件内的连点）', async () => {
    renderSection({ installing: true })
    expect(await screen.findByTestId('node-install-submit')).toBeDisabled()
  })

  it('安装失败后按钮恢复可用（不能永久卡在在途态）', async () => {
    const onInstall = vi.fn(async () => false)
    renderSection({ onInstall })
    const user = userEvent.setup()

    await user.click(await screen.findByTestId('node-install-submit'))
    await waitFor(() => expect(onInstall).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(screen.getByTestId('node-install-submit')).toBeEnabled())
  })
})
