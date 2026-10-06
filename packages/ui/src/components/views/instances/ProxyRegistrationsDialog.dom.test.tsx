import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import ProxyRegistrationsDialog from './ProxyRegistrationsDialog'
import type { ProxyRegistration } from '@jianmanager/ui/lib/proxy-registration'

/**
 * FR-035 代理后端注册管理 · 受控视图测（ADR-097 b 范式）。
 *
 * 应用侧原本只有 1 个「能打开、Esc 能关」的用例；这里补的是**注册语义**：
 * 已注册项渲染、表单校验、错误码到文案的映射（ALIAS_CONFLICT / ALREADY_REGISTERED）、
 * resync 的 secret 不一致与逐条警告。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { close: '关闭', actions: '操作', error: '错误' },
        proxy: {
          manageTitle: '管理后端 — {{name}}',
          resync: '重新同步',
          resynced: '已重新同步到代理与后端',
          resyncFailed: '同步失败',
          secretInconsistent: 'secret 跨 Velocity 代理不一致，modern 转发要求一致，请检查',
          alias: '别名',
          aliasOptional: '可选，缺省取后端名',
          backend: '后端',
          priority: '优先级',
          forcedHost: '强制域名',
          restricted: '限制直连',
          unregister: '取消注册',
          noBackends: '暂无已注册后端',
          registerBackend: '注册后端',
          selectBackend: '选择后端',
          register: '注册',
          registered: '已注册到代理',
          unregistered: '已取消注册',
          aliasConflict: '该代理内别名已占用',
          alreadyRegistered: '该后端已注册进此代理',
        },
        validation: { host: '须为合法主机名或 IP' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

// Combobox 走 Radix 的 use-size，依赖 ResizeObserver；jsdom 未必提供。
beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

type Props = Parameters<typeof ProxyRegistrationsDialog>[0]

function reg(over: Partial<ProxyRegistration> = {}): ProxyRegistration {
  return {
    id: 1,
    proxyId: 10,
    backendId: 101,
    alias: 'lobby',
    priority: 0,
    forcedHost: '',
    restricted: false,
    enabled: true,
    backend: { id: 101, name: 'lobby-01', role: 'backend', nodeId: 1, serverPort: 25566, status: 'RUNNING' },
    ...over,
  }
}

function renderDialog(props: Partial<Props> = {}) {
  const merged = {
    proxyName: 'survival-proxy',
    onClose: vi.fn(),
    registrations: [reg()],
    candidates: [{ id: 102, name: 'lobby-02', serverPort: 25567 }],
    onRegister: vi.fn<Props['onRegister']>().mockResolvedValue({}),
    onUnregister: vi.fn<Props['onUnregister']>().mockResolvedValue(undefined),
    onResync: vi.fn<Props['onResync']>().mockResolvedValue({}),
    notify: vi.fn<Props['notify']>(),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  render(<ProxyRegistrationsDialog {...merged} />, { wrapper: Wrapper })
  return merged
}

/** 打开后端下拉并选中候选项。触发器是普通 button，可访问名取 placeholder。 */
async function pickBackend(user: ReturnType<typeof userEvent.setup>, label = 'lobby-02 (:25567)') {
  await user.click(screen.getByRole('button', { name: '选择后端' }))
  await user.click(await screen.findByText(label))
}

describe('ProxyRegistrationsDialog（FR-035 · ADR-097 b 范式）', () => {
  it('渲染已注册项；无强制域名显示 --，无后端回填则退回 #id', () => {
    renderDialog({
      registrations: [reg(), reg({ id: 2, backendId: 103, alias: 'auth', forcedHost: 'auth.example.com', priority: 5, backend: undefined })],
    })

    expect(screen.getByText('lobby')).toBeInTheDocument()
    expect(screen.getByText('lobby-01')).toBeInTheDocument()
    expect(screen.getByText('auth.example.com')).toBeInTheDocument()
    // 第一条没有强制域名。
    expect(screen.getByText('--')).toBeInTheDocument()
    // 第二条没有后端摘要回填，退回 #id 显示。
    expect(screen.getByText('#103')).toBeInTheDocument()
  })

  it('无已注册后端时给出空态', () => {
    renderDialog({ registrations: [] })
    expect(screen.getByText('暂无已注册后端')).toBeInTheDocument()
  })

  it('未选后端时不能提交', () => {
    renderDialog()
    expect(screen.getByRole('button', { name: '注册' })).toBeDisabled()
  })

  it('强制域名非法时禁用提交并给出错误', async () => {
    const user = userEvent.setup()
    renderDialog()

    await pickBackend(user)
    await user.type(screen.getByPlaceholderText('play.example.com'), 'not a host!')

    expect(screen.getByText('须为合法主机名或 IP')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '注册' })).toBeDisabled()
  })

  it('注册成功：上报载荷、提示成功、表单重置', async () => {
    const user = userEvent.setup()
    const { onRegister, notify } = renderDialog()

    await pickBackend(user)
    await user.type(screen.getByPlaceholderText('lobby'), 'lobby2')
    await user.type(screen.getByPlaceholderText('play.example.com'), 'play.example.com')
    await user.click(screen.getByRole('checkbox'))
    await user.click(screen.getByRole('button', { name: '注册' }))

    await waitFor(() => expect(onRegister).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onRegister).mock.calls[0][0]).toEqual({
      backendId: 102,
      alias: 'lobby2',
      forcedHost: 'play.example.com',
      restricted: true,
    })
    expect(notify).toHaveBeenCalledWith('success', '已注册到代理')
    // 表单已重置：别名输入清空、提交按钮回到禁用（未选后端）。
    expect(screen.getByPlaceholderText('lobby')).toHaveValue('')
    expect(screen.getByRole('button', { name: '注册' })).toBeDisabled()
  })

  it('注册成功但服务端回 warning 时追加警告提示', async () => {
    const user = userEvent.setup()
    const { notify } = renderDialog({
      onRegister: vi.fn<Props['onRegister']>().mockResolvedValue({ warning: '端口与已有注册冲突' }),
    })

    await pickBackend(user)
    await user.click(screen.getByRole('button', { name: '注册' }))

    await waitFor(() => expect(notify).toHaveBeenCalledWith('warning', '端口与已有注册冲突'))
  })

  it('错误码映射：别名冲突与重复注册各给专门文案，其余回退 message', async () => {
    const user = userEvent.setup()
    const { notify } = renderDialog({
      onRegister: vi
        .fn<Props['onRegister']>()
        .mockRejectedValueOnce({ response: { data: { error: 'ALIAS_CONFLICT' } } })
        .mockRejectedValueOnce({ response: { data: { error: 'ALREADY_REGISTERED' } } })
        .mockRejectedValueOnce({ response: { data: { message: '服务端开小差了' } } }),
    })

    // 只选一次后端：注册失败时表单**有意保留**（便于改完重试），故后续轮次触发器仍显示已选后端。
    await pickBackend(user)
    for (const expected of ['该代理内别名已占用', '该后端已注册进此代理', '服务端开小差了']) {
      await user.click(screen.getByRole('button', { name: '注册' }))
      await waitFor(() => expect(notify).toHaveBeenCalledWith('error', expected))
    }
    expect(screen.getByRole('button', { name: '注册' })).toBeEnabled()
  })

  it('取消注册：按 id 上报并提示', async () => {
    const user = userEvent.setup()
    const { onUnregister, notify } = renderDialog({
      registrations: [reg(), reg({ id: 7, backendId: 103, alias: 'auth', backend: undefined })],
    })

    await user.click(screen.getAllByRole('button', { name: '取消注册' })[1])

    await waitFor(() => expect(onUnregister).toHaveBeenCalledWith(7))
    expect(notify).toHaveBeenCalledWith('success', '已取消注册')
  })

  it('resync：secret 不一致给警告，否则给成功', async () => {
    const user = userEvent.setup()
    const { onResync, notify } = renderDialog({
      onResync: vi.fn<Props['onResync']>().mockResolvedValue({ secretConsistent: false, warnings: ['节点 3 未响应'] }),
    })

    await user.click(screen.getByRole('button', { name: '重新同步' }))

    await waitFor(() => expect(onResync).toHaveBeenCalledTimes(1))
    // secret 不一致优先于成功提示，且逐条警告都播报。
    expect(notify).toHaveBeenCalledWith('warning', 'secret 跨 Velocity 代理不一致，modern 转发要求一致，请检查')
    expect(notify).toHaveBeenCalledWith('warning', '节点 3 未响应')
    expect(notify).not.toHaveBeenCalledWith('success', '已重新同步到代理与后端')
  })

  it('resync：secret 一致时给成功提示', async () => {
    const user = userEvent.setup()
    const { notify } = renderDialog({ onResync: vi.fn<Props['onResync']>().mockResolvedValue({ secretConsistent: true }) })

    await user.click(screen.getByRole('button', { name: '重新同步' }))

    await waitFor(() => expect(notify).toHaveBeenCalledWith('success', '已重新同步到代理与后端'))
  })

  it('resync 抛错给失败提示', async () => {
    const user = userEvent.setup()
    const { notify } = renderDialog({ onResync: vi.fn<Props['onResync']>().mockRejectedValue(new Error('boom')) })

    await user.click(screen.getByRole('button', { name: '重新同步' }))

    await waitFor(() => expect(notify).toHaveBeenCalledWith('error', '同步失败'))
  })
})
