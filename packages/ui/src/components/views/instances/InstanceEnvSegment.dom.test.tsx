import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import InstanceEnvSegment from './InstanceEnvSegment'

/**
 * FR-344 环境变量页签 · 受控视图测（ADR-097 b 范式）。
 *
 * 自带局部 i18n（本组件文案面是整个 `env.*` 命名空间且不带 defaultValue），
 * 文案与主控台 zh.json 一致——断言落在这几个键上，故必须与线上同值。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { delete: '删除' },
        env: {
          customTitle: '自定义启动环境变量',
          customHint: '保存后写入实例工作目录 .env 文件，下次启动注入进程生效。',
          empty: '暂无自定义环境变量，点「添加」新增。',
          add: '添加',
          save: '保存',
          keyLabel: '环境变量名',
          valueLabel: '环境变量值',
          runtimeTitle: '运行时实际环境（只读）',
          runtimeUnavailable: '实例未运行或该平台不支持读取运行时进程环境。',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

type Props = Parameters<typeof InstanceEnvSegment>[0]

function renderSegment(props: Partial<Props> = {}) {
  const onSave = vi.fn().mockResolvedValue(true)
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  render(<InstanceEnvSegment runtimeAvailable saving={false} onSave={onSave} {...props} />, { wrapper: Wrapper })
  return { onSave }
}

describe('InstanceEnvSegment（FR-344 · ADR-097 b 范式）', () => {
  it('已配置变量渲染为可编辑行，运行时环境渲染为只读表', () => {
    renderSegment({
      configured: { JAVA_HOME: '/opt/jdks/temurin-21' },
      runtime: { PATH: '/usr/bin', JAVA_HOME: '/opt/jdks/temurin-21' },
      runtimeAvailable: true,
    })

    const keyInputs = screen.getAllByRole('textbox', { name: '环境变量名' }) as HTMLInputElement[]
    expect(keyInputs).toHaveLength(1)
    expect(keyInputs[0].value).toBe('JAVA_HOME')
    // 运行时环境只读表按 key 排序渲染（PATH 在 JAVA_HOME 之后？按 localeCompare：JAVA_HOME < PATH）。
    const rows = screen.getAllByRole('row')
    expect(rows.length).toBeGreaterThanOrEqual(2)
  })

  it('保存时剔除空键，只上报有名字的变量', async () => {
    const user = userEvent.setup()
    const { onSave } = renderSegment({ configured: { KEEP: '1' }, runtimeAvailable: false })

    // 新增一行但不填名字 → 不应进入提交体。
    await user.click(screen.getByRole('button', { name: '添加' }))
    const keyInputs = screen.getAllByRole('textbox', { name: '环境变量名' })
    await user.type(keyInputs[1], '   ')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSave).toHaveBeenCalledWith({ KEEP: '1' }))
  })

  it('删除行后保存，空对象表示清空', async () => {
    const user = userEvent.setup()
    const { onSave } = renderSegment({ configured: { ONLY: 'x' }, runtimeAvailable: false })

    await user.click(screen.getByRole('button', { name: '删除' }))
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSave).toHaveBeenCalledWith({}))
  })

  it('运行环境不可取时展示后端说明（note 优先于兜底文案）', () => {
    renderSegment({ configured: {}, runtimeAvailable: false, note: '实例未运行，无法读取进程环境' })
    expect(screen.getByText('实例未运行，无法读取进程环境')).toBeInTheDocument()
    expect(screen.queryByText('实例未运行或该平台不支持读取运行时进程环境。')).not.toBeInTheDocument()
  })

  it('无自定义变量时给出空态提示', () => {
    renderSegment({ configured: {}, runtimeAvailable: false })
    expect(screen.getByText('暂无自定义环境变量，点「添加」新增。')).toBeInTheDocument()
  })
})
