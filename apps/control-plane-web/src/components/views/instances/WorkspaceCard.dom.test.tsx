import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import WorkspaceCard from '@/components/views/instances/WorkspaceCard'

/**
 * FR-166 统一卡壳 · 受控视图测（ADR-097）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。文案取自 zh.json 原值。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { close: '关闭' },
        workspace: { dragHandle: '拖动以移动卡片', fullscreen: '全屏', exitFullscreen: '退出全屏' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

type Props = Parameters<typeof WorkspaceCard>[0]

function renderCard(props: Partial<Props> = {}) {
  const handlers = {
    onToggleFullscreen: vi.fn(),
    onClose: vi.fn(),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  const result = render(
    <WorkspaceCard
      cardId="card-1"
      title="终端"
      instanceName="survival-01"
      fullscreen={false}
      {...handlers}
    />,
    { wrapper: Wrapper },
  )
  return { ...handlers, ...result }
}

describe('WorkspaceCard（FR-166 · ADR-097）', () => {
  it('渲染注入的标题与实例名，卡内容经 slot 注入', () => {
    const { container } = renderCard({ bodySlot: <div data-testid="body-slot" /> })

    expect(screen.getByText('终端')).toBeInTheDocument()
    expect(screen.getByText('survival-01')).toBeInTheDocument()
    expect(screen.getByTestId('body-slot')).toBeInTheDocument()
    // 卡 id 落在 data-card-id 上（react-grid-layout 的 key 依据）。
    expect(container.querySelector('[data-card-id="card-1"]')).toBeTruthy()
  })

  it('全屏与关闭各自回调外壳', async () => {
    const user = userEvent.setup()
    const { onToggleFullscreen, onClose } = renderCard()

    await user.click(screen.getByRole('button', { name: '全屏' }))
    expect(onToggleFullscreen).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('button', { name: '关闭' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('全屏态下按钮语义翻转为「退出全屏」，且拖拽手柄让位', () => {
    const { container } = renderCard({ fullscreen: true })

    expect(screen.getByRole('button', { name: '退出全屏' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '全屏' })).not.toBeInTheDocument()
    // 全屏时脱离网格：grip 加 pointer-events-none（视觉与交互一并让位）。
    const grip = container.querySelector('.workspace-card-grip')
    expect(grip?.className).toContain('pointer-events-none')
  })

  it('只读卡（导播台）不渲染 grip、全屏与关闭', () => {
    const { container } = renderCard({ readOnly: true })

    expect(container.querySelector('.workspace-card-grip')).toBeNull()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
    // 标题仍在（只读卡头仅显标题）。
    expect(screen.getByText('终端')).toBeInTheDocument()
  })
})
