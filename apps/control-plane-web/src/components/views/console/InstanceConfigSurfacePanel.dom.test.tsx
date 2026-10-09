import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { InstanceConfigSurfacePanel } from '@/components/views/console/InstanceConfigSurfacePanel'
import type { ConfigSurfaceItem } from '@/lib/config-surface'

/**
 * 实例「关键配置」面板 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：加载/空态、分组分节、内联项可编辑、文件引用项只读预览与打开文件链接、
 * 改动计数与保存载荷（含 file→inline 迁移语义）。
 * 端到端（devmock 纵切）由应用侧 `InstanceConfigSurfacePanel.dom.test.tsx` 覆盖。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { loading: '加载中…', save: '保存', saving: '保存中…' },
        configSurface: {
          empty: '暂无可配置项',
          hint: '配置来源明面化',
          otherGroup: '其他',
          sourceLabel: '来源',
          sourceInline: '内联值',
          sourceFile: '文件引用',
          filePath: '文件路径',
          effectiveValue: '生效值',
          previewPending: '待读取',
          previewError: '读取失败：{{message}}',
          openFile: '打开文件',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function item(over: Partial<ConfigSurfaceItem> = {}): ConfigSurfaceItem {
  return {
    itemKey: 'props.max-players',
    source: 'inline',
    registered: true,
    inlineValue: '20',
    effectiveValue: '20',
    effectiveSource: 'inline',
    editable: true,
    group: '网络与身份',
    description: '最大玩家数',
    ...over,
  }
}

function renderPanel(props: Partial<ComponentProps<typeof InstanceConfigSurfacePanel>> = {}) {
  const onSave = vi.fn()
  const renderLink = vi.fn(({ to, className, children }: { to: string; className?: string; children: ReactNode }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ))
  const merged = {
    instanceId: 7,
    items: [item()],
    isLoading: false,
    isSaving: false,
    onSave,
    renderLink,
    ...props,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <InstanceConfigSurfacePanel {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { merged, onSave, renderLink }
}

describe('InstanceConfigSurfacePanel（FR-451 关键配置面板受控视图）', () => {
  it('加载态显示加载文案。', () => {
    renderPanel({ isLoading: true, items: undefined })
    expect(screen.getByText('加载中…')).toBeInTheDocument()
    expect(screen.queryByTestId('config-surface-panel')).toBeNull()
  })

  it('空清单显示空态。', () => {
    renderPanel({ items: [] })
    expect(screen.getByText('暂无可配置项')).toBeInTheDocument()
  })

  it('按分组分节渲染行，展示项名与 itemKey。', () => {
    renderPanel({ items: [item(), item({ itemKey: 'startup.java', description: '启动命令', group: '启动参数' })] })
    expect(screen.getByText('网络与身份')).toBeInTheDocument()
    expect(screen.getByText('启动参数')).toBeInTheDocument()
    expect(screen.getByTestId('config-surface-row-props.max-players')).toBeInTheDocument()
    expect(screen.getByText('props.max-players')).toBeInTheDocument()
  })

  it('内联项：改值后保存按钮计数 +1，载荷带新值。', async () => {
    const user = userEvent.setup()
    const { onSave } = renderPanel()
    const save = screen.getByTestId('config-surface-save')
    // 未改动时保存不可用。
    expect(save).toBeDisabled()

    await user.clear(screen.getByLabelText('最大玩家数'))
    await user.type(screen.getByLabelText('最大玩家数'), '30')
    expect(screen.getByTestId('config-surface-save')).toHaveTextContent('(1)')

    await user.click(screen.getByTestId('config-surface-save'))
    expect(onSave).toHaveBeenCalledWith([{ itemKey: 'props.max-players', source: 'inline', inlineValue: '30' }])
  })

  it('有 choices 的内联项渲染下拉而非自由输入。', () => {
    renderPanel({ items: [item({ choices: ['true', 'false'], inlineValue: 'true' })] })
    expect(screen.getByRole('combobox', { name: '最大玩家数' })).toBeInTheDocument()
  })

  it('文件引用项：只读展示生效值与打开文件链接（走注入的 renderLink）。', () => {
    const { renderLink } = renderPanel({
      items: [
        item({
          source: 'file',
          inlineValue: undefined,
          filePath: 'server.properties',
          fileKey: 'props.max-players',
          effectiveSource: 'file',
          effectiveValue: '42',
        }),
      ],
    })
    expect(screen.getByText('42')).toBeInTheDocument()
    expect(renderLink).toHaveBeenCalled()
    const link = screen.getByRole('link', { name: /打开文件/ })
    expect(link).toHaveAttribute('href', '/instances/7/files?path=server.properties')
  })

  it('文件读取失败时如实提示，不假装有生效值。', () => {
    renderPanel({
      items: [
        item({
          source: 'file',
          inlineValue: undefined,
          filePath: 'server.properties',
          effectiveSource: 'file',
          effectiveValue: '',
          previewError: '文件不存在',
        }),
      ],
    })
    expect(screen.getByText('读取失败：文件不存在')).toBeInTheDocument()
  })

  it('切来源为文件：保存载荷带 filePath 与 fileKey。', async () => {
    const user = userEvent.setup()
    const { onSave } = renderPanel({
      items: [item({ fileKey: 'props.max-players' })],
    })
    await user.click(screen.getByRole('button', { name: '文件引用' }))
    await user.click(screen.getByTestId('config-surface-save'))
    expect(onSave).toHaveBeenCalledWith([
      { itemKey: 'props.max-players', source: 'file', filePath: 'server.properties', fileKey: 'props.max-players' },
    ])
  })

  it('保存中：按钮禁用并显示保存中文案。', () => {
    renderPanel({ isSaving: true })
    expect(screen.getByTestId('config-surface-save')).toHaveTextContent('保存中…')
  })

  it('启动项不提供「文件引用」来源（后端同样拒绝）。', () => {
    renderPanel({ items: [item({ itemKey: 'startup.java', source: 'inline', description: '启动命令' })] })
    const row = screen.getByTestId('config-surface-row-startup.java')
    expect(within(row).queryByRole('button', { name: '文件引用' })).toBeNull()
    expect(within(row).getByLabelText('启动命令')).toBeInTheDocument()
  })
})
