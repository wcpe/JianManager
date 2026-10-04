import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import i18next from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { describe, expect, it, vi } from 'vitest'

import { Button } from './button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from './dialog'

/**
 * Dialog 现状快照测试（FR-496 阶段 0）。
 *
 * 共享 Dialog 是全站弹窗语义的唯一出口（Radix 组合件 + role=dialog），阶段 4 重构时最容易
 * 静默破坏的是三件事：① 门户挂载点（内容必须经 Portal 挂到 body，而不是留在渲染容器里）；
 * ② 可访问名/描述与 Title/Description 的关联；③ 关闭入口集合。
 *
 * 关闭入口尤其重要：组件源码明确规定"表单型 Dialog 点遮罩/外部不关闭，仅 取消 / 右上角 X /
 * ESC 关闭"（避免下拉展开时误关整个弹窗），所以"点遮罩后仍然打开"是一条**必须被钉死的正向契约**，
 * 而不是顺手省略的用例——重构时给 Radix 补上 outsideClick 关闭是最常见的回归。
 *
 * 断言只落在角色、可访问性属性与 `data-slot` 钩子上，不涉及任何样式类名。
 */

/** 打开一个标准表单型对话框供各用例复用（Title/Description 齐全，避免 Radix 的可访问性告警）。 */
function renderDialog(
  options: {
    showCloseButton?: boolean
    footerClose?: boolean
    /** 覆盖关闭按钮文案（阶段 6 加入的可选入参）。 */
    closeLabel?: string
    /** DialogContent 显式拦截并转发的外部按下回调（组件源码里会先 preventDefault 再回调）。 */
    onPointerDownOutside?: (event: Event) => void
  } = {},
) {
  return render(
    <Dialog>
      <DialogTrigger asChild>
        <Button>打开对话框</Button>
      </DialogTrigger>
      <DialogContent
        showCloseButton={options.showCloseButton}
        closeLabel={options.closeLabel}
        onPointerDownOutside={options.onPointerDownOutside}
      >
        <DialogHeader>
          <DialogTitle>编辑实例</DialogTitle>
          <DialogDescription>保存后需重启实例生效</DialogDescription>
        </DialogHeader>
        <DialogFooter showCloseButton={options.footerClose} closeLabel={options.closeLabel}>
          <Button>保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>,
  )
}

/** 同上，但用于需要观察外部点击回调的用例。 */
type DialogOptions = Parameters<typeof renderDialog>[0]

/** 点触发器打开对话框，等门户内容就绪后返回渲染结果与对话框节点。 */
async function openDialog(options: DialogOptions = {}) {
  const view = renderDialog(options)
  fireEvent.click(screen.getByRole('button', { name: '打开对话框' }))
  const dialog = await screen.findByRole('dialog')
  return { ...view, dialog }
}

/**
 * 让 Radix 真正挂上 document 级的 pointerdown 监听器。
 *
 * @radix-ui/react-dismissable-layer 的 usePointerDownOutside 把监听器注册在 `setTimeout(…, 0)` 里，
 * 且 DialogContent 传了 deferPointerDownOutside（外部点击延迟到随后的 click 才判定）；
 * 而 findBy 系列与 waitFor 首轮命中就 resolve，只推进微任务队列、不推进宏任务队列。
 * 不显式等一个宏任务，"点遮罩没关"这类**负向断言会假绿**（事件打在尚未挂载的监听器上）。
 */
async function flushMacrotask() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

describe('Dialog', () => {
  it('关闭态不渲染任何对话框内容', () => {
    renderDialog()

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.querySelector('[data-slot="dialog-portal"]')).toBeNull()
    expect(document.querySelector('[data-slot="dialog-overlay"]')).toBeNull()
    expect(document.querySelector('[data-slot="dialog-content"]')).toBeNull()
  })

  it('打开后经门户挂到 body，并带齐遮罩/内容钩子', async () => {
    const { container, dialog } = await openDialog()

    // 内容被 Portal 送到 body：渲染容器里查不到它，文档体里能查到
    expect(container.querySelector('[data-slot="dialog-content"]')).toBeNull()
    expect(document.body).toContainElement(dialog)

    expect(dialog).toHaveAttribute('data-slot', 'dialog-content')
    expect(document.querySelector('[data-slot="dialog-overlay"]')).not.toBeNull()

    // 注意：源码在 DialogPortal 上挂了 data-slot="dialog-portal"，但 Radix 的 DialogPortal
    // 只读取 container / forceMount / children，其余 props 被直接丢弃，所以该钩子从未落进 DOM。
    // 这属于现状缺陷（而非契约），故不断言它的存在或缺失——重构时把钩子接回去是改进，不该被测试拦住。
  })

  it('Title / Description 关联为对话框的可访问名与描述', async () => {
    await openDialog()

    const dialog = screen.getByRole('dialog', { name: '编辑实例' })
    expect(dialog).toHaveAccessibleName('编辑实例')
    expect(dialog).toHaveAccessibleDescription('保存后需重启实例生效')
  })

  it('Header / Title / Description / Footer 插槽钩子齐全', async () => {
    const { dialog } = await openDialog()

    expect(dialog.querySelector('[data-slot="dialog-header"]')).not.toBeNull()
    expect(dialog.querySelector('[data-slot="dialog-footer"]')).not.toBeNull()
    expect(screen.getByRole('heading', { name: '编辑实例' })).toHaveAttribute('data-slot', 'dialog-title')
    expect(screen.getByText('保存后需重启实例生效')).toHaveAttribute('data-slot', 'dialog-description')
  })

  it('默认渲染右上角关闭按钮，点击即关闭', async () => {
    await openDialog()

    const closeButtons = document.querySelectorAll('[data-slot="dialog-close"]')
    expect(closeButtons).toHaveLength(1)
    // 图标按钮的可见文案是 sr-only 的 Close，作为可访问名暴露
    expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument()

    fireEvent.click(closeButtons[0])
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('showCloseButton={false} 时不渲染右上角关闭按钮', async () => {
    await openDialog({ showCloseButton: false })

    // 页脚默认也不带关闭按钮，故整棵门户里没有任何关闭入口
    expect(document.querySelector('[data-slot="dialog-close"]')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('点遮罩（外部）不关闭，ESC 可关闭', async () => {
    const onPointerDownOutside = vi.fn()
    await openDialog({ onPointerDownOutside })
    await flushMacrotask()

    const overlay = document.querySelector('[data-slot="dialog-overlay"]') as HTMLElement
    fireEvent.pointerDown(overlay)
    fireEvent.click(overlay)

    // 先证明这次点击真的打到了 Radix 的外部按下链路（否则下面的"没关"会因事件根本没送达而假绿）
    expect(onPointerDownOutside).toHaveBeenCalledTimes(1)
    // 再锁定既有契约：外部点击被 no-op 掉，表单型弹窗保持打开
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    fireEvent.keyDown(document, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('DialogFooter showCloseButton 与 DialogClose 都能关闭对话框', async () => {
    // ① 页脚关闭按钮：showCloseButton 的对外可见结果（注意它走的是裸 Radix Close，
    //    不带 data-slot="dialog-close" 钩子，故此处只按可访问名定位——现状即如此）
    await openDialog({ showCloseButton: false, footerClose: true })
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    // ② 自定义关闭入口：DialogClose 包裹任意子元素后点击同样能关闭
    render(
      <Dialog>
        <DialogTrigger asChild>
          <Button>打开取消用例</Button>
        </DialogTrigger>
        <DialogContent showCloseButton={false}>
          <DialogHeader>
            <DialogTitle>确认删除</DialogTitle>
            <DialogDescription>删除后不可恢复</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="ghost">取消</Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>,
    )
    fireEvent.click(screen.getByRole('button', { name: '打开取消用例' }))
    await screen.findByRole('dialog')

    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })
})

/**
 * FR-496 阶段 6：阶段 0 记录的三个 Dialog 缺陷修复 + 关闭文案 i18n。
 *
 * 缺陷本身不影响功能，但都会让"下一层"的消费者踩坑：死钩子让人以为能选到门户、
 * 钩子不对称让脚本只抓得到一个关闭入口、硬编码英文让中英混排的界面漏出一处英文。
 */
describe('Dialog 钩子与关闭文案（FR-496 阶段 6 修复）', () => {
  it('打开后门户钩子真正落进 DOM（阶段 0 的死钩子已修复）', async () => {
    const { dialog } = await openDialog()

    const portal = document.querySelector('[data-slot="dialog-portal"]')
    expect(portal).not.toBeNull()
    // display:contents：只承载标记，不产生布局盒（不会让 fixed 定位的兄弟节点错位）
    expect(portal).toHaveClass('contents')
    // 遮罩与内容都在门户内：钩子标的正是它们共同的挂载子树
    expect(portal).toContainElement(document.querySelector('[data-slot="dialog-overlay"]'))
    expect(portal).toContainElement(dialog)
  })

  it('关闭态不残留门户节点', () => {
    renderDialog()

    expect(document.querySelector('[data-slot="dialog-portal"]')).toBeNull()
  })

  it('页脚关闭按钮与右上角 X 的 dialog-close 钩子对称', async () => {
    await openDialog({ showCloseButton: false, footerClose: true })

    const closes = document.querySelectorAll('[data-slot="dialog-close"]')
    expect(closes).toHaveLength(1)
    // 仍然是带 outline 变体的 Button，没有被换成裸 Radix 按钮（样式与按压反馈都还在）
    expect(closes[0].tagName).toBe('BUTTON')
    expect(closes[0]).toHaveAttribute('data-variant', 'outline')
    expect(closes[0]).toHaveTextContent('Close')
  })

  it('closeLabel 同时覆盖右上角 X 与页脚关闭文案', async () => {
    await openDialog({ closeLabel: '关闭', footerClose: true })

    expect(screen.getAllByRole('button', { name: '关闭' })).toHaveLength(2)
  })

  it('未接 i18n 的宿主回落英文 Close，页脚与右上角一致', async () => {
    await openDialog({ footerClose: true })

    expect(screen.getAllByRole('button', { name: 'Close' })).toHaveLength(2)
  })

  it('宿主 i18n 生效时关闭文案跟随语言（common.close）', async () => {
    // 用独立实例 + Provider：既验证真实 i18n 链路，又不污染 react-i18next 全局单例
    //（同文件其它用例依赖"无实例→英文回落"这条路径）
    const zhI18n = i18next.createInstance()
    await zhI18n.init({
      resources: { zh: { translation: { common: { close: '关闭' } } } },
      lng: 'zh',
    })

    render(
      <I18nextProvider i18n={zhI18n}>
        <Dialog>
          <DialogTrigger asChild>
            <Button>打开对话框</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>编辑实例</DialogTitle>
              <DialogDescription>保存后需重启实例生效</DialogDescription>
            </DialogHeader>
            <DialogFooter showCloseButton>
              <Button>保存</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </I18nextProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: '打开对话框' }))
    await screen.findByRole('dialog')

    expect(screen.getAllByRole('button', { name: '关闭' })).toHaveLength(2)
  })
})
