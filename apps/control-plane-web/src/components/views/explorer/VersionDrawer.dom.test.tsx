import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import VersionDrawer, { type VersionDrawerProps } from '@/components/views/explorer/VersionDrawer'
import type { FileVersion } from '@jianmanager/ui/lib/file-version'

/**
 * FR-070 历史版本抽屉 · 受控视图测（ADR-097 c 范式）。
 *
 * 应用侧 `VersionDrawer.dom.test.tsx` mock 取数 hook 验联动；
 * 这里补的是**受控契约**：列表渲染（含回滚来源标注）、对比选择上报、
 * diff 三态（加载/二进制/文本）、回滚二次确认后上报与成功提示。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        files: { loading: '加载中…' },
        fileVersions: {
          title: '历史版本', loadFailed: '加载失败', empty: '暂无历史版本',
          diffFrom: '对比', diffTo: '到', rollback: '回滚', rollbackVia: '回滚自',
          diffTitle: '差异', binary: '二进制内容无法对比',
          rollbackSuccess: '已回滚到 #{{version}}', rollbackFailed: '回滚失败',
          rollbackTitle: '确认回滚', rollbackConfirm: '把 {{name}} 回滚到 #{{version}}？',
        },
        common: { delete: '删除', cancel: '取消' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

beforeAll(() => {
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

function version(over: Partial<FileVersion> = {}): FileVersion {
  return {
    id: 7, filePath: 'server.properties', size: 2048,
    authorId: 1, createdAt: '2025-01-02T03:04:05Z', ...over,
  }
}

function renderDrawer(over: Partial<VersionDrawerProps> = {}) {
  const props: VersionDrawerProps = {
    filePath: 'server.properties',
    open: true,
    onOpenChange: vi.fn(),
    versions: [version()],
    versionsLoading: false,
    versionsFailed: false,
    onDiffSelect: vi.fn(),
    diffLoading: false,
    onRollback: vi.fn().mockResolvedValue(undefined),
    rollbackPending: false,
    notify: vi.fn(),
    ...over,
  }
  render(
    <I18nextProvider i18n={testI18n}>
      <VersionDrawer {...props} />
    </I18nextProvider> as ReactNode,
  )
  return props
}

describe('VersionDrawer（FR-070 历史版本抽屉）', () => {
  it('渲染版本行的 ID、大小与回滚来源标注。', () => {
    renderDrawer({ versions: [version({ id: 9, size: 3072, rollbackOfVersionId: 5 })] })
    expect(screen.getByText('#9')).toBeInTheDocument()
    expect(screen.getByText('3.0 KB')).toBeInTheDocument()
    expect(screen.getByText(/回滚自/)).toBeInTheDocument()
  })

  it('三态：加载中 / 加载失败 / 空列表。', () => {
    const { unmount } = render(
      <I18nextProvider i18n={testI18n}>
        <VersionDrawer
          {...({
            filePath: 'a.txt', open: true, onOpenChange: vi.fn(),
            versions: [], versionsLoading: true, versionsFailed: false,
            onDiffSelect: vi.fn(), diffLoading: false,
            onRollback: vi.fn(), rollbackPending: false, notify: vi.fn(),
          } as VersionDrawerProps)}
        />
      </I18nextProvider> as ReactNode,
    )
    expect(screen.getByText('加载中…')).toBeInTheDocument()
    unmount()

    renderDrawer({ versions: [], versionsFailed: true })
    expect(screen.getByText('加载失败')).toBeInTheDocument()
  })

  it('点「对比」/「到」把两个版本号上报给外壳。', async () => {
    const user = userEvent.setup()
    const props = renderDrawer({ versions: [version({ id: 3 }), version({ id: 4 })] })
    const rows = screen.getAllByRole('listitem')
    await user.click(within(rows[0]).getByRole('button', { name: '对比' }))
    expect(props.onDiffSelect).toHaveBeenCalledWith(3, null)
    await user.click(within(rows[1]).getByRole('button', { name: '到' }))
    expect(props.onDiffSelect).toHaveBeenLastCalledWith(3, 4)
  })

  it('两版本选定且不同时展示 diff：文本走 UnifiedDiff、二进制给提示。', async () => {
    const user = userEvent.setup()
    const { unmount } = render(
      <I18nextProvider i18n={testI18n}>
        <VersionDrawer
          {...({
            filePath: 'a.txt', open: true, onOpenChange: vi.fn(),
            versions: [version({ id: 3 }), version({ id: 4 })],
            versionsLoading: false, versionsFailed: false,
            onDiffSelect: vi.fn(), diffLoading: false,
            diff: { fromVersionId: 3, toVersionId: 4, unifiedDiff: '@@ -1 +1 @@\n-a\n+b', binary: true },
            onRollback: vi.fn(), rollbackPending: false, notify: vi.fn(),
          } as VersionDrawerProps)}
        />
      </I18nextProvider> as ReactNode,
    )
    const rows = screen.getAllByRole('listitem')
    await user.click(within(rows[0]).getByRole('button', { name: '对比' }))
    await user.click(within(rows[1]).getByRole('button', { name: '到' }))
    expect(await screen.findByText('二进制内容无法对比')).toBeInTheDocument()
    unmount()
  })

  it('回滚经二次确认后上报版本号并提示成功。', async () => {
    const user = userEvent.setup()
    const props = renderDrawer({ versions: [version({ id: 11 })] })
    await user.click(screen.getByRole('button', { name: '回滚' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '回滚' }))
    expect(props.onRollback).toHaveBeenCalledWith(11)
    expect(props.notify).toHaveBeenCalledWith('success', '已回滚到 #11')
  })

  it('回滚失败时透出服务端 message。', async () => {
    const user = userEvent.setup()
    const notify = vi.fn()
    renderDrawer({
      versions: [version({ id: 12 })],
      notify,
      onRollback: vi.fn().mockRejectedValue({ response: { data: { message: '快照空间不足' } } }),
    })
    await user.click(screen.getByRole('button', { name: '回滚' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '回滚' }))
    expect(notify).toHaveBeenCalledWith('error', '快照空间不足')
  })
})
