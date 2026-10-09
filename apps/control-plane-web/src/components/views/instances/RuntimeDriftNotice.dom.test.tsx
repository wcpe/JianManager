import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { RuntimeDriftAdoptButton, RuntimeDriftBadge, RuntimeDriftBanner } from '@/components/views/instances/RuntimeDriftNotice'

/**
 * FR-471 运行态漂移提示 · 受控视图测（ADR-097）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。文案取自 zh.json 原值。
 * 重点覆盖「接管」的两道闸：权限门禁（canOperate）与二次确认（DangerConfirm）。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', delete: '删除', irreversible: '此操作不可撤销。' },
        permissions: { operateDenied: '你没有操作该实例的权限' },
        danger: { denied: '权限不足，无法执行该操作' },
        serverConsole: {
          runtimeDriftBadge: '运行态漂移',
          runtimeDriftBadgeTip: '工作目录下存在未被平台纳管的活进程（PID {{pid}}），面板状态可能与实际不一致',
          runtimeDriftTitle: '运行态漂移：存在未纳管进程',
          runtimeDriftDesc: '该实例工作目录下有 PID {{pid}} 的活进程未被平台纳管。',
          runtimeDriftCmdline: '进程命令行',
          runtimeDriftAdopt: '接管',
          runtimeDriftAdoptHint: '停止该未纳管进程并以受管方式重新拉起（会重启该服）',
          runtimeDriftAdoptTitle: '接管实例「{{name}}」的运行态？',
          runtimeDriftAdoptDesc: '将先优雅停止 PID {{pid}} 的未纳管进程，再以平台受管方式重新拉起。',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function Wrapper({ children }: { children: ReactNode }) {
  return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
}

describe('RuntimeDriftNotice（FR-471 · ADR-097）', () => {
  it('紧凑徽章带 PID 提示，且不含接管入口', () => {
    render(<RuntimeDriftBadge pid={4242} cmdline="java -jar server.jar" />, { wrapper: Wrapper })

    const badge = screen.getByTestId('runtime-drift-badge')
    expect(badge).toBeInTheDocument()
    expect(badge.getAttribute('title')).toContain('PID 4242')
    expect(badge.getAttribute('title')).toContain('java -jar server.jar')
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('无操作权限时接管按钮禁用', () => {
    render(
      <RuntimeDriftAdoptButton instanceName="s1" pid={1} canOperate={false} onAdopt={vi.fn()} />,
      { wrapper: Wrapper },
    )
    expect(screen.getByTestId('runtime-drift-adopt')).toBeDisabled()
  })

  it('接管按钮先开二次确认，确认后才上报', async () => {
    const user = userEvent.setup()
    const onAdopt = vi.fn().mockResolvedValue(true)
    render(
      <RuntimeDriftAdoptButton instanceName="survival-01" pid={4242} canOperate onAdopt={onAdopt} />,
      { wrapper: Wrapper },
    )

    await user.click(screen.getByTestId('runtime-drift-adopt'))
    expect(await screen.findByText('接管实例「survival-01」的运行态？')).toBeInTheDocument()
    // 未确认前不得下发。
    expect(onAdopt).not.toHaveBeenCalled()

    const dialog = document.querySelector('[role="dialog"]')!
    const confirm = Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent?.trim() === '接管')!
    await user.click(confirm)
    await waitFor(() => expect(onAdopt).toHaveBeenCalledTimes(1))
  })

  it('在途时按钮禁用，避免重复下发', () => {
    render(
      <RuntimeDriftAdoptButton instanceName="s1" pid={1} canOperate adopting onAdopt={vi.fn()} />,
      { wrapper: Wrapper },
    )
    expect(screen.getByTestId('runtime-drift-adopt')).toBeDisabled()
  })

  it('横幅展示 PID 与命令行，并内嵌接管入口', () => {
    render(
      <RuntimeDriftBanner
        instanceName="survival-01"
        pid={4242}
        cmdline="java -jar server.jar"
        canOperate
        onAdopt={vi.fn()}
      />,
      { wrapper: Wrapper },
    )

    const banner = screen.getByTestId('runtime-drift-banner')
    expect(banner.getAttribute('role')).toBe('alert')
    expect(banner.textContent).toContain('PID 4242')
    expect(banner.textContent).toContain('java -jar server.jar')
    expect(screen.getByTestId('runtime-drift-adopt')).toBeInTheDocument()
  })

  it('无命令行时不渲染命令行行', () => {
    render(
      <RuntimeDriftBanner instanceName="s1" pid={7} canOperate onAdopt={vi.fn()} />,
      { wrapper: Wrapper },
    )
    expect(screen.getByTestId('runtime-drift-banner').textContent).not.toContain('进程命令行')
  })
})
