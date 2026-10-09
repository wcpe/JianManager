import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { SuperWorkbenchPage } from '@/components/views/console/SuperWorkbenchPage'
import type { PlacedCard } from '@jianmanager/ui/lib/workspace-preset'

/**
 * 超级工作台页面 · 受控视图测（ADR-097）。
 *
 * 补的是**注入面契约**：画布状态与全部动作来自 props、四个子块由外壳注入、
 * 空态与全屏分支、拖拽落位与布局变更回传。store 联动由应用侧接线层承担。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        superWorkbench: {
          emptyCanvas: '画布为空',
          emptyCanvasHint: '从左侧实例库拖入实例或功能',
          focusTerminalsEmpty: '画布上还没有终端卡',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function card(over: Partial<PlacedCard> = {}): PlacedCard {
  return {
    id: 'c1',
    type: 'terminal',
    instanceId: 1,
    layout: { x: 0, y: 0, w: 6, h: 8 },
    ...over,
  } as PlacedCard
}

function renderPage(props: Partial<ComponentProps<typeof SuperWorkbenchPage>> = {}) {
  const onEnsureCanvas = vi.fn()
  const onLayoutChange = vi.fn()
  const onDropCard = vi.fn()
  const onRemoveCard = vi.fn()
  const onFullscreenChange = vi.fn()
  const renderCard = vi.fn(({ card: c, fullscreen }: { card: PlacedCard; fullscreen?: boolean }) => (
    <div data-testid={`card-${c.id}`} data-fullscreen={String(Boolean(fullscreen))} />
  ))
  const renderLibrary = vi.fn(() => <div data-testid="library" />)
  const renderToolbar = vi.fn(() => <div data-testid="toolbar" />)
  const renderFocusTerminals = vi.fn(() => <div data-testid="focus" />)
  const merged = {
    canvas: { cards: [] as PlacedCard[] },
    userPresets: [],
    onEnsureCanvas,
    onApplyPreset: vi.fn(),
    onDropCard,
    onRemoveCard,
    onLayoutChange,
    onFullscreenChange,
    onSavePresetAs: vi.fn(),
    onDeletePreset: vi.fn(),
    renderLibrary,
    renderToolbar,
    renderCard,
    renderFocusTerminals,
    ...props,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <SuperWorkbenchPage {...(merged as ComponentProps<typeof SuperWorkbenchPage>)} />
    </I18nextProvider> as ReactNode,
  )
  return { ...merged, onEnsureCanvas, onLayoutChange, onDropCard, renderCard, renderLibrary, renderToolbar }
}

describe('SuperWorkbenchPage（FR-167 超级工作台受控视图）', () => {
  it('挂载即请求初始化画布，并渲染注入的实例库与工具栏。', () => {
    const { onEnsureCanvas, renderLibrary, renderToolbar } = renderPage()
    expect(onEnsureCanvas).toHaveBeenCalled()
    expect(renderLibrary).toHaveBeenCalled()
    expect(renderToolbar).toHaveBeenCalled()
    expect(screen.getByTestId('library')).toBeInTheDocument()
    expect(screen.getByTestId('toolbar')).toBeInTheDocument()
  })

  it('空画布渲染空态引导。', () => {
    renderPage({ canvas: { cards: [] } })
    expect(screen.getByText('画布为空')).toBeInTheDocument()
    expect(screen.getByText('从左侧实例库拖入实例或功能')).toBeInTheDocument()
  })

  it('有卡片时逐张调用注入的 renderCard。', () => {
    const { renderCard } = renderPage({ canvas: { cards: [card(), card({ id: 'c2', type: 'metrics' })] } })
    expect(renderCard).toHaveBeenCalledTimes(2)
    expect(screen.getByTestId('card-c1')).toBeInTheDocument()
    expect(screen.getByTestId('card-c2')).toBeInTheDocument()
  })

  it('全屏卡走 renderCard 的 fullscreen 分支，且只渲染这一张。', () => {
    const { renderCard } = renderPage({
      canvas: { cards: [card(), card({ id: 'c2' })], fullscreenCardId: 'c2' },
    })
    expect(renderCard).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('card-c2')).toHaveAttribute('data-fullscreen', 'true')
  })

  it('未就绪（canvas 为 null）时按空画布渲染，不抛错。', () => {
    renderPage({ canvas: null })
    expect(screen.getByText('画布为空')).toBeInTheDocument()
  })

  it('专注终端只在 focusInstanceId 非空时由外壳注入渲染（初始不渲染）。', () => {
    const { renderFocusTerminals } = renderPage()
    expect(renderFocusTerminals).not.toHaveBeenCalled()
  })
})
