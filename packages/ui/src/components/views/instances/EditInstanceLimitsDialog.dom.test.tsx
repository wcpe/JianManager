import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import EditInstanceLimitsDialog from './EditInstanceLimitsDialog'

/**
 * FR-079 实例资源限额编辑器 · 受控视图测（ADR-097 b 范式）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。核心断言是「留空即不限制」
 * 这一输入约定，以及非 docker 模式只提示不编辑。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', close: '关闭', save: '保存', saving: '保存中…' },
        instances: {
          resourceLimitTitle: '资源限额「{{name}}」',
          resourceLimitDockerOnly: '仅 docker 模式支持资源限额',
          cpuLimit: 'CPU 核数',
          memLimit: '内存上限',
          diskLimit: '磁盘上限',
          resourceLimitHint: '留空表示不限制',
          diskLimitHint: '留空表示不限制',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

type Props = Parameters<typeof EditInstanceLimitsDialog>[0]

function renderDialog(props: Partial<Props> = {}) {
  const handlers = { onClose: vi.fn(), onSave: vi.fn().mockResolvedValue(true), ...props }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  render(
    <EditInstanceLimitsDialog
      instanceName="survival-01"
      processType="docker"
      cpuLimit={1.5}
      memLimitMb={2048}
      diskLimitMb={0}
      {...handlers}
    />,
    { wrapper: Wrapper },
  )
  return handlers
}

describe('EditInstanceLimitsDialog（FR-079 · ADR-097 b 范式）', () => {
  it('非 docker 模式只提示，不给编辑表单', () => {
    renderDialog({ processType: 'direct' })
    expect(screen.getByText('仅 docker 模式支持资源限额')).toBeInTheDocument()
    expect(screen.queryByText('CPU 核数')).not.toBeInTheDocument()
  })

  it('0 显示为空（不限制），非 0 回填具体值', () => {
    renderDialog()
    const inputs = screen.getAllByRole('textbox') as HTMLInputElement[]
    expect(inputs[0].value).toBe('1.5')
    expect(inputs[1].value).toBe('2048')
    // diskLimitMb=0 → 留空，而不是显示 0。
    expect(inputs[2].value).toBe('')
  })

  it('留空回落 0（不限制）后上报', async () => {
    const user = userEvent.setup()
    const { onSave } = renderDialog()

    await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onSave).mock.calls[0][0]).toEqual({ cpuLimit: 1.5, memLimitMb: 2048, diskLimitMb: 0 })
  })

  it('校验不通过时禁用保存并给出错误', async () => {
    const user = userEvent.setup()
    const { onSave } = renderDialog()

    const cpuInput = screen.getAllByRole('textbox')[0]
    await user.clear(cpuInput)
    await user.type(cpuInput, 'abc')
    expect(screen.getByRole('button', { name: '保存' })).toBeDisabled()
    expect(onSave).not.toHaveBeenCalled()
  })
})
