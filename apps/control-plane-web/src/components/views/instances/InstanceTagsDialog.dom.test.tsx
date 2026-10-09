import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { render } from '@testing-library/react'
import InstanceTagsDialog from '@/components/views/instances/InstanceTagsDialog'

/**
 * FR-047 实例标签编辑器 · 受控视图测（ADR-097 b 范式）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。断言聚焦本组件自己的编辑语义：
 * 标签拆分、env: 前缀归一（防双份环境标签）、合并顺序（环境在前）、保存上报。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', save: '保存', saving: '保存中…' },
        grouping: {
          tagsTitle: '编辑「{{name}}」的标签',
          environment: '环境',
          envNone: '无',
          freeTags: '自由标签',
          noTags: '暂无标签',
          removeTag: '移除标签',
          addTagPlaceholder: '输入标签后回车',
          addTag: '添加',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function renderDialog(props: Partial<Parameters<typeof InstanceTagsDialog>[0]> = {}) {
  const handlers = { onClose: vi.fn(), onSave: vi.fn().mockResolvedValue(true), ...props }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  render(<InstanceTagsDialog instanceName="survival-01" tags={['env:prod', '生产', 'region:r1']} {...handlers} />, {
    wrapper: Wrapper,
  })
  return handlers
}

describe('InstanceTagsDialog（FR-047 · ADR-097 b 范式）', () => {
  it('把既有标签拆成环境与自由标签两部分', () => {
    renderDialog()
    // env: 前缀的进环境选择，其余作为自由标签 chip（不重复出现环境标签）。
    expect(screen.getByText('生产')).toBeInTheDocument()
    expect(screen.getByText('region:r1')).toBeInTheDocument()
    expect(screen.queryByText('env:prod')).not.toBeInTheDocument()
  })

  it('输入 env: 前缀的草稿归一到环境选择，不落成自由标签', async () => {
    const user = userEvent.setup()
    renderDialog()

    const input = screen.getByPlaceholderText('输入标签后回车')
    await user.type(input, 'env:dev')
    await user.click(screen.getByRole('button', { name: '添加' }))

    // 不应作为自由标签出现（否则会产生双份环境标签）。
    // 注意：不能顺带断言「dev」不存在——Radix Select 的环境选项里本来就有 dev。
    expect(screen.queryByText('env:dev')).not.toBeInTheDocument()
  })

  it('保存时环境标签在前、自由标签随后', async () => {
    const user = userEvent.setup()
    const { onSave } = renderDialog()

    await user.type(screen.getByPlaceholderText('输入标签后回车'), '新增')
    await user.click(screen.getByRole('button', { name: '添加' }))
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(vi.mocked(onSave).mock.calls[0][0]).toEqual(['env:prod', '生产', 'region:r1', '新增'])
  })

  it('保存成功后才关闭', async () => {
    const user = userEvent.setup()
    const { onClose } = renderDialog({ onSave: vi.fn().mockResolvedValue(false) })
    await user.click(screen.getByRole('button', { name: '保存' }))
    // 失败时保持打开，用户可修正后重试。
    await waitFor(() => expect(onClose).not.toHaveBeenCalled())
  })
})
