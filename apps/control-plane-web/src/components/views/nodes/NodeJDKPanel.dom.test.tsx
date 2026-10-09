import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import NodeJDKPanel, {
  type NodeJDKView,
  type ProbeResultView,
} from './NodeJDKPanel'

/**
 * FR-033 / FR-311 节点 JDK 管理 · 受控视图测（ADR-097 b 范式）。
 *
 * 与迁移前的关键差异（**有意的取舍，勿当缺陷「修掉」**）：
 * 1. 列表与探测结果都由 props / 回调返回值提供，故「登记后列表出现新 JDK」改为
 *    断言「onRegister 收到正确请求体」+「视图回到列表分段」——列表归外壳所有。
 * 2. 原先断言里「同路径出现多处」是因为本面板与运行时库分区同时展示同一条 JDK；
 *    受控化后运行时库由 `runtimeSlot` 注入，本测试不注入它，故不再有重复计数断言。
 *
 * i18n 只备断言用到的键（其余键组件里带 defaultValue），且文案与主控台 zh.json 一致。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { save: '保存', cancel: '取消', delete: '删除' },
        nodes: {
          jdkManaged: '托管',
          jdkExternal: '外部',
          jdkMarkManaged: '标记为 Worker 托管（仅作记录）',
          jdkVersion: '版本号',
          jdkPath: '本地路径',
        },
        artifactCache: {
          jdkTab: { list: '已登记', install: '一键下载', register: '登记已有' },
        },
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

type PanelProps = Parameters<typeof NodeJDKPanel>[0]

function renderPanel(props: Partial<PanelProps> = {}) {
  const handlers: PanelProps = {
    onInstall: vi.fn().mockResolvedValue(true),
    onRegister: vi.fn().mockResolvedValue(true),
    onProbe: vi.fn().mockResolvedValue(null),
    onUpdate: vi.fn().mockResolvedValue(true),
    onDelete: vi.fn().mockResolvedValue(true),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  const result = render(<NodeJDKPanel {...handlers} />, { wrapper: Wrapper })
  return { ...handlers, ...result }
}

const jdks: NodeJDKView[] = [
  { id: 1, vendor: 'Temurin', majorVersion: 21, version: '21.0.4+9', arch: 'x64', path: '/opt/jdks/temurin-21', managed: true },
  { id: 2, vendor: 'Temurin', majorVersion: 17, version: '17.0.11+9', arch: 'x64', path: '/opt/jdks/temurin-17', managed: true },
]

const probeOk: ProbeResultView = {
  valid: true,
  vendor: 'Temurin',
  majorVersion: 21,
  version: '21.0.99',
  arch: 'x64',
  javaHome: '/opt/jdks/custom-java-21',
}

describe('NodeJDKPanel（FR-033 · ADR-097 b 范式）', () => {
  it('展示已登记 JDK，探测通过后上报登记请求并回到列表分段', async () => {
    const user = userEvent.setup()
    const onProbe = vi.fn().mockResolvedValue(probeOk)
    const onRegister = vi.fn().mockResolvedValue(true)
    renderPanel({ jdks, onProbe, onRegister })

    expect(screen.getAllByText('Temurin').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('Java 21')).toBeInTheDocument()
    expect(screen.getByText('/opt/jdks/temurin-21')).toBeInTheDocument()
    // 来源筛选 chip 显示托管计数（两条种子均为托管）。
    expect(screen.getByRole('button', { name: /托管2/ })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '登记已有' }))
    expect(screen.getByLabelText('标记为 Worker 托管（仅作记录）')).toBeInTheDocument()

    await user.type(screen.getByPlaceholderText('/opt/jdks/temurin-21 或 .../bin/java'), '/opt/jdks/custom-java-21')
    await user.click(screen.getByRole('button', { name: '检测' }))

    await waitFor(() => expect(onProbe).toHaveBeenCalledWith('/opt/jdks/custom-java-21'))
    // 探测结果只读展示（版本与 JDK 根路径）。
    expect(await screen.findByText('21.0.99')).toBeInTheDocument()
    expect(screen.getByText('/opt/jdks/custom-java-21')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(onRegister).toHaveBeenCalledTimes(1))
    expect(onRegister.mock.calls[0][0]).toMatchObject({
      vendor: 'Temurin',
      majorVersion: 21,
      version: '21.0.99',
      arch: 'x64',
      path: '/opt/jdks/custom-java-21',
      managed: false,
    })
    // 登记成功后视图回到列表分段。
    await waitFor(() => expect(screen.queryByLabelText('标记为 Worker 托管（仅作记录）')).not.toBeInTheDocument())
  })

  it('探测失败时展示错误且保持保存禁用', async () => {
    const user = userEvent.setup()
    const onProbe = vi.fn().mockResolvedValue({
      valid: false,
      vendor: '',
      majorVersion: 0,
      version: '',
      arch: '',
      javaHome: '',
      error: 'not a jdk home',
    } satisfies ProbeResultView)
    renderPanel({ jdks, onProbe })

    await user.click(screen.getByRole('button', { name: '登记已有' }))
    await user.type(screen.getByPlaceholderText('/opt/jdks/temurin-21 或 .../bin/java'), '/opt/invalid-runtime')
    await user.click(screen.getByRole('button', { name: '检测' }))

    expect(await screen.findByText(/所选目录不是有效的 JDK/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '保存' })).toBeDisabled()
  })

  it('行内编辑：改版本与路径后上报，成功则关闭模态', async () => {
    const user = userEvent.setup()
    const onUpdate = vi.fn().mockResolvedValue(true)
    renderPanel({ jdks, onUpdate })

    await user.click(screen.getAllByRole('button', { name: '编辑' })[0])
    expect(await screen.findByText('编辑 JDK 登记信息')).toBeInTheDocument()

    const verInput = screen.getByRole('textbox', { name: '版本号' })
    await user.clear(verInput)
    await user.type(verInput, '21.0.99')
    const pathInput = screen.getByRole('textbox', { name: '本地路径' })
    await user.clear(pathInput)
    await user.type(pathInput, '/opt/jdks/temurin-21-edited')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(1))
    expect(onUpdate.mock.calls[0][0]).toBe(1)
    expect(onUpdate.mock.calls[0][1]).toMatchObject({
      vendor: 'Temurin',
      version: '21.0.99',
      path: '/opt/jdks/temurin-21-edited',
    })
    await waitFor(() => expect(screen.queryByText('编辑 JDK 登记信息')).not.toBeInTheDocument())
  })

  it('删除托管项走 DangerConfirm：确认前需逐字输入「厂商 主版本」', async () => {
    const user = userEvent.setup()
    const onDelete = vi.fn().mockResolvedValue(true)
    renderPanel({ jdks, onDelete })

    await user.click(screen.getAllByRole('button', { name: '删除' })[0])
    expect(await screen.findByText('删除 JDK（含文件）?')).toBeInTheDocument()
    const dialog = document.querySelector('[role="dialog"]')!
    const confirm = Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent?.includes('删除'))!
    expect(confirm).toBeDisabled()
    await user.type(dialog.querySelector('input')!, 'Temurin 21')
    await waitFor(() => expect(confirm).toBeEnabled())
    await user.click(confirm)

    await waitFor(() => expect(onDelete).toHaveBeenCalledTimes(1))
    expect(onDelete.mock.calls[0][0]).toMatchObject({ id: 1, managed: true })
  })
})
