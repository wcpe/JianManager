import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import BinaryVersionPanel, { type BinaryVersionView } from './BinaryVersionPanel'

/**
 * FR-468 实例二进制版本区 · 受控视图测（ADR-097 b 范式）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。状态类断言锚在 data-testid 上
 * （文案面很宽，绑它们会让测试变脆）；而「确认按钮」必须按文案精确定位——弹窗里同时存在
 * 取消、X 关闭与确认三个按钮，按位置取会随 DialogContent 结构变化而错位。
 * 文案取自 zh.json 原值。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { loading: '加载中…', refresh: '刷新', cancel: '取消' },
        serverConsole: {
          binaryVersionTitle: '二进制版本',
          binaryVersionHint: '版本真源为制品库；升级与回滚均为显式受控操作',
          binaryVersionCurrent: '当前版本',
          binaryVersionPrevious: '上一版本',
          binaryVersionPickTarget: '选择目标版本',
          binaryVersionTargetLabel: '目标版本',
          binaryVersionUpgrade: '升级',
          binaryVersionRollback: '回滚上一版本',
          binaryVersionStopHint: '升级与回滚要求实例已停止；变更完成后需手动启动',
          binaryVersionUpgradeConfirmTitle: '确认升级二进制版本？',
          binaryVersionUpgradeConfirmBody: '将把可执行文件替换为 {{target}}。',
          binaryVersionRollbackConfirmTitle: '确认回滚二进制版本？',
          binaryVersionRollbackConfirmBody: '将回滚到 {{target}}。',
          binaryVersionLoadFailed: '读取二进制版本失败',
          binaryVersionUnbound: '该实例未登记版本绑定',
          binaryVersionNoLibrary: '该来源没有制品库版本',
          binaryVersionDrift: '检测到版本漂移',
          binaryVersionDiskChecked: '已核对磁盘文件摘要',
          binaryVersionDiskUnchecked: '本次未核对磁盘文件',
          binaryVersionDiskSkipped: '跳过磁盘核对',
          binaryVersionWriteDenied: '需要实例写权限',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

const view: BinaryVersionView = {
  bound: true,
  currentAssetId: 7,
  currentVersion: '1.1.0',
  currentFilename: 'beacon-1.1.0-linux-amd64',
  currentSha256: 'abcdef0123456789abcdef0123456789',
  hasRollback: true,
  previousAssetId: 6,
  previousVersion: '1.0.0',
  previousFilename: 'beacon-1.0.0-linux-amd64',
  driftDetected: false,
  diskSha256Checked: true,
  noLibraryVersion: false,
  candidates: [{ assetId: 9, version: '1.2.0', filename: 'beacon-1.2.0-linux-amd64' }],
}

type Props = Parameters<typeof BinaryVersionPanel>[0]

function renderPanel(props: Partial<Props> = {}) {
  const handlers = {
    onRefresh: vi.fn(),
    onUpgrade: vi.fn().mockResolvedValue(true),
    onRollback: vi.fn().mockResolvedValue(true),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  const result = render(<BinaryVersionPanel view={view} {...handlers} />, { wrapper: Wrapper })
  return { ...handlers, ...result }
}

describe('BinaryVersionPanel（FR-468 · ADR-097 b 范式）', () => {
  it('渲染当前版本与上一版本', () => {
    renderPanel()
    expect(screen.getByTestId('binary-version-current')).toHaveTextContent('1.1.0')
    expect(screen.getByTestId('binary-version-previous')).toHaveTextContent('1.0.0')
    expect(screen.getByTestId('binary-version-disk-checked')).toBeInTheDocument()
  })

  it('未绑定版本时给出说明而非空壳', () => {
    renderPanel({ view: { ...view, bound: false, note: '该实例未登记版本绑定' } })
    expect(screen.getByTestId('binary-version-unbound')).toHaveTextContent('该实例未登记版本绑定')
  })

  it('读取失败时给出错误块与刷新入口', async () => {
    const user = userEvent.setup()
    const { onRefresh } = renderPanel({ isError: true, view: undefined })
    expect(screen.getByTestId('binary-version-error')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '刷新' }))
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('实例在运行时禁用升级与回滚', () => {
    renderPanel({ instanceLive: true })
    expect(screen.getByRole('button', { name: '升级' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '回滚上一版本' })).toBeDisabled()
  })

  it('无写权限时禁用（权限优先于运行态给出原因）', () => {
    renderPanel({ canWrite: false })
    expect(screen.getByRole('button', { name: '升级' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '回滚上一版本' })).toBeDisabled()
  })

  it('升级需逐字输入目标版本号，确认后才上报', async () => {
    const user = userEvent.setup()
    const onUpgrade = vi.fn().mockResolvedValue(true)
    renderPanel({ onUpgrade })

    // 选目标版本 → 点升级 → 弹确认框。
    await user.selectOptions(screen.getByLabelText('目标版本'), '9')
    await user.click(screen.getByRole('button', { name: '升级' }))

    expect(await screen.findByText('确认升级二进制版本？')).toBeInTheDocument()
    const dialog = screen.getByRole('dialog')
    const confirm = Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent?.trim() === '升级')!
    // confirmText 门禁：未逐字输入目标版本号前，确认按钮必须禁用。
    expect(confirm).toBeDisabled()
    expect(onUpgrade).not.toHaveBeenCalled()

    await user.type(dialog.querySelector('input')!, '1.2.0')
    await waitFor(() => expect(confirm).toBeEnabled())
    await user.click(confirm)

    await waitFor(() => expect(onUpgrade).toHaveBeenCalledWith(9))
  })

  it('回滚同样经二次确认后上报', async () => {
    const user = userEvent.setup()
    const onRollback = vi.fn().mockResolvedValue(true)
    renderPanel({ onRollback })

    await user.click(screen.getByRole('button', { name: '回滚上一版本' }))
    expect(await screen.findByText('确认回滚二进制版本？')).toBeInTheDocument()

    const dialog = screen.getByRole('dialog')
    const confirm = Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent?.trim() === '回滚上一版本')!
    await user.type(dialog.querySelector('input')!, '1.0.0')
    await waitFor(() => expect(confirm).toBeEnabled())
    await user.click(confirm)

    await waitFor(() => expect(onRollback).toHaveBeenCalledTimes(1))
  })
})
