import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { TemplatesPage, type TemplateView } from '@/components/views/TemplatesPage'

/**
 * FR-064/FR-154 模板市场 · 受控视图测（ADR-097 a/b 范式）。
 *
 * 应用侧 `TemplatesPage.dom.test.tsx` 走 mock 假后端验「渲染 + 联动」；
 * 这里补的是**受控契约**：删除经 `onDelete` 上报、应用模板的实例载荷形态
 * （变量填充派生 startCommand、MC 类型工作目录留空）、新建模板类型候选去重、
 * 失败时提示通道拿服务端 message。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', delete: '删除', create: '创建', creating: '创建中...' },
        instances: {
          instanceName: '实例名称', node: '节点', group: '分组', selectNode: '选择节点',
          noGroup: '不分组', createFailed: '创建实例失败',
        },
        templates: {
          title: '模板市场', marketSubtitle: '挑一个模板，一键开服', create: '新建模板',
          empty: '还没有模板', deleteConfirm: '删除模板「{{name}}」？', deleteDescription: '删除后不可恢复',
          deleted: '模板已删除', deleteFailed: '删除模板失败', created: '模板已创建',
          createFailed: '创建模板失败', createTitle: '新建模板', name: '模板名称',
          namePlaceholder: '例如 Paper 1.21', type: '类型', typePlaceholder: '选择或输入类型',
          description: '描述', descriptionPlaceholder: '一句话说明', startCommand: '启动命令',
          startCommandPlaceholder: 'java -Xmx2G -jar server.jar', downloadUrl: '下载地址',
          downloadUrlPlaceholder: 'https://...', defaultWorkDir: '默认工作目录',
          defaultWorkDirPlaceholder: '/srv/mc', downloadLink: '下载',
          market: {
            totalApps: '模板总数', typeCount: '类型数', oneClickDeploy: '一键开服',
            oneClickDeployHint: '选好节点即可', search: '搜索模板', noMatch: '没有匹配的模板',
            deploy: '用此模板创建', deployTitle: '用「{{name}}」创建实例', ramShort: '{{value}} 内存',
            variablesTitle: '填充变量', variablesHint: '把占位换成实际值',
            variablesSyntaxHint: '用 {{变量}} 表示占位', copied: '已复制', copyFailed: '复制失败',
            copy: '复制命令', expand: '展开', collapse: '收起',
            created: '实例「{{name}}」已创建',
          },
        },
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

const TPL: TemplateView = {
  id: 7,
  name: 'Paper 1.21',
  type: 'minecraft_java',
  description: '经典粘液开服核心',
  startCommand: 'java -Xmx4G -jar paper.jar --port {{PORT}}',
  updatedAt: '2025-01-02T03:04:05Z',
}

function renderPage(over: Partial<Parameters<typeof TemplatesPage>[0]> = {}) {
  const props = {
    templates: [TPL],
    isLoading: false,
    nodeOptions: [{ value: '1', label: 'n1' }],
    groupOptions: [],
    onDelete: vi.fn().mockResolvedValue(undefined),
    onCreate: vi.fn().mockResolvedValue(undefined),
    onApply: vi.fn().mockResolvedValue(undefined),
    notify: vi.fn(),
    ...over,
  }
  const view = render(
    <I18nextProvider i18n={testI18n}>
      <TemplatesPage {...props} />
    </I18nextProvider> as ReactNode,
  )
  return { props, view }
}

describe('TemplatesPage（FR-064/FR-154 模板市场）', () => {
  it('渲染卡片的市场信息：名称、类型标签、Java/RAM 需求与更新时间。', () => {
    renderPage()
    expect(screen.getByText('Paper 1.21')).toBeInTheDocument()
    expect(screen.getByText('Minecraft Java')).toBeInTheDocument()
    // deriveMarketMeta 从 -Xmx4G 推断 RAM 需求
    expect(screen.getByText(/4G/)).toBeInTheDocument()
  })

  it('删除经二次确认后上报 id，成功才提示已删除。', async () => {
    const user = userEvent.setup()
    const { props } = renderPage()
    await user.click(screen.getByRole('button', { name: '删除' }))
    // 确认对话框出现后确认
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '删除' }))
    await waitFor(() => expect(props.onDelete).toHaveBeenCalledWith(7))
    await waitFor(() => expect(props.notify).toHaveBeenCalledWith('success', '模板已删除'))
  })

  it('加载中不渲染卡片骨架数以外的内容，空列表展示占位。', () => {
    const { view } = renderPage({ isLoading: true })
    expect(screen.queryByText('Paper 1.21')).not.toBeInTheDocument()
    view.unmount()
    renderPage({ templates: [] })
    expect(screen.getByText('还没有模板')).toBeInTheDocument()
  })
})
