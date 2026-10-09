import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import CreateBotDialog from '@/components/views/instances/CreateBotDialog'

/**
 * FR-039 新建 Bot 对话框 · 受控视图测（ADR-097 b 范式）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。断言聚焦本组件自己的编辑语义：
 * 建议值预填与覆盖、必填校验、载荷结构（port 转数值），以及**「创建成功但委托失败」**
 * 这条真机踩过的分支——它必须留在弹窗内显示原因，而不是静默关窗留下 pending Bot。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', create: '创建', creating: '创建中...' },
        bots: {
          createBot: '创建 Bot',
          instance: '实例',
          name: '名称',
          serverAddr: '服务器地址',
          port: '端口',
          authMethod: '认证方式',
          initialBehavior: '初始行为',
          offline: '离线',
          microsoft: 'Microsoft',
          idle: '空闲',
          guard: '守卫',
          follow: '跟随',
          patrol: '巡逻',
          createFailed: '创建失败',
          delegateFailed: '已创建记录，但节点侧启动失败（原因未知）',
        },
        validation: {
          required: '此项必填',
          host: '须为合法主机名或 IP',
          portInteger: '端口须为整数',
          portRange: '端口须在 1–65535 之间',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

type Props = Parameters<typeof CreateBotDialog>[0]

function renderDialog(props: Partial<Props> = {}) {
  const merged = {
    open: true,
    onOpenChange: vi.fn<(open: boolean) => void>(),
    instanceName: 'survival-01',
    suggestedServer: 'mc.example.com',
    suggestedPort: 25565,
    creating: false,
    onCreate: vi.fn<Props['onCreate']>().mockResolvedValue({ ok: true }),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  render(<CreateBotDialog {...merged} />, { wrapper: Wrapper })
  return merged
}

describe('CreateBotDialog（FR-039 · ADR-097 b 范式）', () => {
  it('归属实例名与建议连接地址、端口都预填好', () => {
    renderDialog()

    expect(screen.getByDisplayValue('survival-01')).toBeInTheDocument()
    expect(screen.getByDisplayValue('mc.example.com')).toBeInTheDocument()
    expect(screen.getByRole('spinbutton')).toHaveValue(25565)
    // 两个下拉的默认选中项（Radix Select 另有隐藏原生 option，故锚在 combobox 上）。
    const combos = screen.getAllByRole('combobox')
    expect(combos).toHaveLength(2)
    expect(combos[0]).toHaveTextContent('离线')
    expect(combos[1]).toHaveTextContent('空闲')
  })

  it('改动连接地址即覆盖建议值', async () => {
    const user = userEvent.setup()
    renderDialog()

    const server = screen.getByDisplayValue('mc.example.com')
    await user.clear(server)
    await user.type(server, '10.0.0.7')

    expect(screen.getByDisplayValue('10.0.0.7')).toBeInTheDocument()
  })

  it('必填为空时禁用提交并给出错误', async () => {
    const user = userEvent.setup()
    const { onCreate } = renderDialog()

    // 名称为空 → 提交按钮直接禁用，且错误已显示（不必等提交）。
    expect(screen.getByRole('button', { name: '创建' })).toBeDisabled()
    expect(screen.getByText('此项必填')).toBeInTheDocument()

    await user.type(screen.getByPlaceholderText('GuardBot'), 'GuardBot')
    expect(screen.getByRole('button', { name: '创建' })).toBeEnabled()
    expect(onCreate).not.toHaveBeenCalled()
  })

  it('提交把端口转成数值并按结构上报', async () => {
    const user = userEvent.setup()
    const { onCreate } = renderDialog()

    await user.type(screen.getByPlaceholderText('GuardBot'), 'GuardBot')
    await user.click(screen.getByRole('button', { name: '创建' }))

    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onCreate).mock.calls[0][0]).toEqual({
      name: 'GuardBot',
      server: 'mc.example.com',
      port: 25565,
      auth: 'offline',
      behavior: 'idle',
    })
  })

  it('创建成功但节点侧委托失败：不关窗，留在弹窗内显示原因', async () => {
    const user = userEvent.setup()
    const { onOpenChange } = renderDialog({
      onCreate: vi.fn<Props['onCreate']>().mockResolvedValue({ ok: false, error: 'bot 依赖未安装' }),
    })

    await user.type(screen.getByPlaceholderText('GuardBot'), 'GuardBot')
    await user.click(screen.getByRole('button', { name: '创建' }))

    expect(await screen.findByText('bot 依赖未安装')).toBeInTheDocument()
    expect(onOpenChange).not.toHaveBeenCalledWith(false)
    // 名称保留，用户可直接改完重试。
    expect(screen.getByDisplayValue('GuardBot')).toBeInTheDocument()
  })

  it('创建成功后关窗', async () => {
    const user = userEvent.setup()
    const { onOpenChange } = renderDialog()

    await user.type(screen.getByPlaceholderText('GuardBot'), 'GuardBot')
    await user.click(screen.getByRole('button', { name: '创建' }))

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  })

  it('取消关窗', async () => {
    const user = userEvent.setup()
    const { onCreate, onOpenChange } = renderDialog()

    await user.click(screen.getByRole('button', { name: '取消' }))

    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(onCreate).not.toHaveBeenCalled()
  })

  it('创建在途时按钮显示进行中文案并禁用', () => {
    renderDialog({ creating: true })

    const btn = screen.getByRole('button', { name: '创建中...' })
    expect(btn).toBeDisabled()
  })
})
