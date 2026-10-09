import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import EditInstanceConfigDialog from '@/components/views/instances/EditInstanceConfigDialog'

/**
 * FR-233 实例配置编辑器 · 受控视图测（ADR-097 b 范式）。
 *
 * 应用侧原本只有 1 个「能打开、Esc 能关」的用例；这里补的是**编辑语义**：
 * 初值回填、JDK「留空=解绑」、保存载荷、成功才关窗、失败保留弹窗并优先显示服务端 message。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', save: '保存', saving: '保存中...' },
        instances: {
          editConfigTitle: '编辑实例配置 · {{name}}',
          jdkBinding: '绑定 JDK',
          jdkBindingHint: '为实例绑定 JDK（重绑可解「未绑定 JDK / Java 版本不符崩溃」）；留空=用系统 Java。',
          configSaved: '配置已保存（下次启动生效）',
          configSaveFailed: '保存配置失败',
          jdkSystemDefault: '不指定（使用系统 Java）',
        },
        instanceDetail: { startCommand: '启动命令', autoRestart: '崩溃自动重启' },
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

type Props = Parameters<typeof EditInstanceConfigDialog>[0]

const jdkOptions = [
  { value: '3', label: 'Temurin 21 (21.0.2)' },
  { value: '4', label: 'Temurin 17 (17.0.9)' },
]

function renderDialog(props: Partial<Props> = {}) {
  const merged = {
    instanceName: 'docker-mc',
    startCommand: 'java -jar server.jar nogui',
    jdkId: 0,
    autoRestart: false,
    onClose: vi.fn(),
    jdkOptions,
    onSave: vi.fn<Props['onSave']>().mockResolvedValue(undefined),
    notify: vi.fn<Props['notify']>(),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  render(<EditInstanceConfigDialog {...merged} />, { wrapper: Wrapper })
  return merged
}

/** 打开 JDK 下拉并选中某一项。触发器是普通 button，可访问名取 placeholder。 */
async function pickJdk(user: ReturnType<typeof userEvent.setup>, label: string) {
  await user.click(screen.getByRole('button', { name: '不指定（使用系统 Java）' }))
  await user.click(await screen.findByText(label))
}

describe('EditInstanceConfigDialog（FR-233 · ADR-097 b 范式）', () => {
  it('回填当前配置：启动命令、自动重启、未绑定 JDK 时显示系统默认', () => {
    renderDialog({ autoRestart: true })

    expect(screen.getByRole('heading', { name: '编辑实例配置 · docker-mc' })).toBeInTheDocument()
    expect(screen.getByDisplayValue('java -jar server.jar nogui')).toBeInTheDocument()
    expect(screen.getByRole('checkbox')).toBeChecked()
    expect(screen.getByRole('button', { name: '不指定（使用系统 Java）' })).toBeInTheDocument()
  })

  it('已绑定 JDK 时下拉显示该 JDK', () => {
    renderDialog({ jdkId: 3 })
    // 选中项成为触发器文本（不再是 placeholder）。
    expect(screen.getByRole('button', { name: 'Temurin 21 (21.0.2)' })).toBeInTheDocument()
  })

  it('保存上报改动，JDK 转数值', async () => {
    const user = userEvent.setup()
    const { onSave, onClose, notify } = renderDialog()

    await user.clear(screen.getByDisplayValue('java -jar server.jar nogui'))
    await user.type(screen.getByPlaceholderText('java -Xmx2G -jar server.jar nogui'), 'java -Xmx4G -jar server.jar')
    await pickJdk(user, 'Temurin 17 (17.0.9)')
    await user.click(screen.getByRole('checkbox'))
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onSave).mock.calls[0][0]).toEqual({
      startCommand: 'java -Xmx4G -jar server.jar',
      jdkId: 4,
      autoRestart: true,
    })
    expect(notify).toHaveBeenCalledWith('success', '配置已保存（下次启动生效）')
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('未绑定 JDK 时提交 jdkId=0（系统默认）', async () => {
    const user = userEvent.setup()
    const { onSave } = renderDialog({ jdkId: 0 })

    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    // 本地状态为空串 → 载荷回 0，即「解绑 / 用系统 Java」这一语义。
    expect(vi.mocked(onSave).mock.calls[0][0].jdkId).toBe(0)
  })

  it('保存失败：优先显示服务端 message，且不关窗', async () => {
    const user = userEvent.setup()
    const { onClose, notify } = renderDialog({
      onSave: vi.fn<Props['onSave']>().mockRejectedValue({ response: { data: { message: '节点离线' } } }),
    })

    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(notify).toHaveBeenCalledWith('error', '节点离线'))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('保存失败且无服务端 message 时回退通用文案', async () => {
    const user = userEvent.setup()
    const { notify } = renderDialog({ onSave: vi.fn<Props['onSave']>().mockRejectedValue(new Error('boom')) })

    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(notify).toHaveBeenCalledWith('error', '保存配置失败'))
  })

  it('取消关窗且不上报', async () => {
    const user = userEvent.setup()
    const { onClose, onSave } = renderDialog()

    await user.click(screen.getByRole('button', { name: '取消' }))

    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onSave).not.toHaveBeenCalled()
  })
})
