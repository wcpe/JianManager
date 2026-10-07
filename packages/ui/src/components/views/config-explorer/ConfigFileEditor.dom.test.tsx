import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { ConfigFileEditor } from './ConfigFileEditor'

/**
 * 配置编辑器 · 受控视图测（ADR-097）。
 *
 * 补的是**注入面契约**：读取态（加载/失败/有数据）、文本模式的编辑器由外壳注入、
 * dirty 上报与关闭/版本入口。取数与写入由应用侧接线层承担，
 * 端到端（含 schema 表单与搜索跳转）由应用侧 `ConfigFileEditor.dom.test.tsx` 覆盖。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { loading: '加载中…', save: '保存' },
        configExplorer: {
          reverted: '已还原',
          crossCheck: '跨文件校验',
          textMode: '原文',
          formMode: '表单',
          versions: '版本',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function renderEditor(props: Partial<ComponentProps<typeof ConfigFileEditor>> = {}) {
  const onDirtyChange = vi.fn()
  const onWrite = vi.fn()
  const renderCodeEditor = vi.fn(({ value, onChange }: { value: string; onChange: (v: string) => void }) => (
    <textarea data-testid="injected-editor" value={value} onChange={(e) => onChange(e.target.value)} />
  ))
  const merged = {
    path: 'server.properties',
    name: 'server.properties',
    onClose: vi.fn(),
    onAfterSave: vi.fn(),
    onOpenVersions: vi.fn(),
    onDirtyChange,
    readData: {
      path: 'server.properties',
      content: 'motd=hello',
      format: 'properties',
      fields: [],
      validation: { valid: true, issues: [] },
    },
    onWrite,
    onWriteFields: vi.fn(),
    onCrossCheck: vi.fn(async () => []),
    renderCodeEditor,
    ...props,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <ConfigFileEditor {...(merged as ComponentProps<typeof ConfigFileEditor>)} />
    </I18nextProvider> as ReactNode,
  )
  return { ...merged, onDirtyChange, onWrite, renderCodeEditor }
}

describe('ConfigFileEditor（FR-071 单文件配置编辑器受控视图）', () => {
  it('读取中显示加载文案，不渲染编辑器。', () => {
    renderEditor({ readData: undefined, isReadLoading: true })
    expect(screen.getByText('加载中…')).toBeInTheDocument()
    expect(screen.queryByTestId('injected-editor')).toBeNull()
  })

  it('读取失败时显式展示原因，不静默。', () => {
    renderEditor({ readData: undefined, readError: '文件不存在' })
    expect(screen.getByText('文件不存在')).toBeInTheDocument()
  })

  it('文本模式：编辑器由注入的 renderCodeEditor 提供，初值为读取内容。', () => {
    const { renderCodeEditor } = renderEditor()
    expect(renderCodeEditor).toHaveBeenCalled()
    expect(screen.getByTestId('injected-editor')).toHaveValue('motd=hello')
  })

  it('校验有效时展示 valid 徽章。', () => {
    renderEditor()
    expect(screen.getByText('valid')).toBeInTheDocument()
  })

  it('校验失败时展示 invalid 徽章。', () => {
    renderEditor({
      readData: {
        path: 'server.properties',
        content: 'x',
        format: 'properties',
        fields: [],
        validation: { valid: false, issues: [{ key: 'motd', message: 'bad' } as never] },
      },
    })
    expect(screen.getByText('invalid')).toBeInTheDocument()
  })
})
