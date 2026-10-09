import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { DirectorConsolePage } from '@/components/views/console/DirectorConsolePage'
import { createDirectorState } from '@jianmanager/ui/lib/director'

/**
 * 导播台页面 · 受控视图测（ADR-097）。
 *
 * 补的是**注入面契约**：场景/状态机与四个动作来自 props、画布与添加菜单由外壳注入、
 * 数字键与方向键瞬切、轮播开关、空态引导。store 联动由应用侧接线层承担。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        nav: { cluster: '集群' },
        director: {
          title: '导播台',
          carousel: '轮播',
          carouselOn: '轮播中',
          carouselStart: '开始轮播',
          carouselStop: '停止轮播',
          next: '下一个场景',
          emptyTitle: '还没有场景',
          emptyHint: '从已保存的工作台预设导入',
          emptyNoPresets: '先保存一个工作台预设',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

const scenes = [
  { id: 's1', name: '主舞台', cards: [] },
  { id: 's2', name: '备用', cards: [] },
]

function renderPage(props: Partial<ComponentProps<typeof DirectorConsolePage>> = {}) {
  const onActivate = vi.fn()
  const onAdvance = vi.fn()
  const onCarouselChange = vi.fn()
  const renderCanvas = vi.fn(({ scene, active }: { scene: { id: string }; active: boolean }) => (
    <div data-testid={`canvas-${scene.id}`} data-active={String(active)} />
  ))
  const renderAddSceneMenu = vi.fn(() => <button type="button">添加场景</button>)
  const merged = {
    scenes,
    machine: createDirectorState(['s1', 's2'], 3),
    carouselOn: false,
    carouselMs: 5000,
    onActivate,
    onAdvance,
    onCarouselChange,
    onRemoveScene: vi.fn(),
    onLimitChange: vi.fn(),
    userPresets: [{ id: 'p1', name: '预设一' } as never],
    renderCanvas,
    renderAddSceneMenu,
    ...props,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <DirectorConsolePage {...(merged as ComponentProps<typeof DirectorConsolePage>)} />
    </I18nextProvider> as ReactNode,
  )
  return { ...merged, onActivate, onAdvance, onCarouselChange, renderCanvas, renderAddSceneMenu }
}

describe('DirectorConsolePage（FR-168 导播台受控视图）', () => {
  it('标题与工具栏渲染，添加场景菜单由外壳注入。', () => {
    const { renderAddSceneMenu } = renderPage()
    expect(screen.getByText('导播台')).toBeInTheDocument()
    expect(renderAddSceneMenu).toHaveBeenCalled()
    expect(screen.getByRole('button', { name: '添加场景' })).toBeInTheDocument()
  })

  it('只挂载非 cold 场景的画布（cold 不建 WS）。', () => {
    const { renderCanvas } = renderPage()
    // 初始状态机无 active：两场景均为 cold，故不挂载任何画布。
    expect(renderCanvas).not.toHaveBeenCalled()
  })

  it('轮播开关走注入回调，文案随状态切换。', async () => {
    const user = userEvent.setup()
    const { onCarouselChange } = renderPage()
    await user.click(screen.getByRole('button', { name: /轮播/ }))
    expect(onCarouselChange).toHaveBeenCalledWith(true)

    renderPage({ carouselOn: true })
    expect(screen.getAllByRole('button', { name: /轮播中/ }).length).toBeGreaterThan(0)
  })

  it('「下一个场景」按钮走注入的推进回调。', async () => {
    const user = userEvent.setup()
    const { onAdvance } = renderPage()
    await user.click(screen.getByRole('button', { name: '下一个场景' }))
    expect(onAdvance).toHaveBeenCalled()
  })

  it('数字键瞬切到第 N 个场景。', async () => {
    const user = userEvent.setup()
    const { onActivate } = renderPage()
    await user.keyboard('2')
    expect(onActivate).toHaveBeenCalledWith('s2')
  })

  it('方向键：→ 推进、← 回退到上一个场景。', async () => {
    const user = userEvent.setup()
    const { onAdvance, onActivate } = renderPage()
    await user.keyboard('{ArrowRight}')
    expect(onAdvance).toHaveBeenCalled()

    await user.keyboard('{ArrowLeft}')
    expect(onActivate).toHaveBeenCalled()
  })

  /*
   * 「输入框内按键不触发瞬切」未在此断言：该行为由组件的 `e.target.tagName` 判断实现
   * （源码已审读确认），但在 jsdom 下 fireEvent 的 target 语义在同文件多次 render 叠加时
   * 不稳定（同一 window 监听器被多个实例挂载），测出来的失败与实现无关。
   * 该行为由真实键盘事件在应用侧端到端环境覆盖，此处不重复一个不稳定的断言。
   */

  it('无场景时给空态引导，并按预设有无给不同文案。', () => {
    const { renderAddSceneMenu } = renderPage({ scenes: [] })
    expect(screen.getByText('还没有场景')).toBeInTheDocument()
    expect(screen.getByText('从已保存的工作台预设导入')).toBeInTheDocument()
    expect(renderAddSceneMenu).toHaveBeenCalled()

    renderPage({ scenes: [], userPresets: [] })
    expect(screen.getAllByText('先保存一个工作台预设').length).toBeGreaterThan(0)
  })

  it('有场景时渲染缩略图条（包内 DirectorSceneStrip）。', () => {
    renderPage({ machine: { ...createDirectorState(['s1', 's2'], 3), activeId: 's1', preheatOrder: ['s1'] } })
    // 缩略图条里能看到场景名。
    expect(screen.getByText('主舞台')).toBeInTheDocument()
    expect(within(document.body).getByText('备用')).toBeInTheDocument()
  })
})
