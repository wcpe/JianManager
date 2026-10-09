import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import NodeRuntimeSection, {
  type RuntimeCandidateView,
  type RuntimeRowView,
} from '@/components/views/nodes/NodeRuntimeSection'

/**
 * FR-298 节点运行时库 · 受控视图测（ADR-097 b 范式）。
 *
 * 本文件自带局部 i18n 实例：本组件的文案面是整个 `nodes.runtimeLib.*` 命名空间，
 * 且这些键在组件里**不带 defaultValue**，塞进共享实例会让它变成第二份应用语言包。
 * 文案与主控台 zh.json 逐字对齐——否则测试通过也说明不了线上表现。
 *
 * 与迁移前的关键差异（**有意的取舍，勿当缺陷「修掉」**）：列表与候选都由 props /
 * 回调返回值提供，视图无权失效重取，因此「入库后列表出现 Node.js 行」这类断言改为
 * 断言**回调收到什么**、以及**视图根据返回值做了什么**（关闭模态、禁用按钮）。
 * 数据联动属外壳的集成测范围。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: { cancel: '取消', delete: '删除' },
        nodes: {
          jdkManaged: '托管',
          jdkExternal: '外部',
          runtimeLib: {
            title: '运行时',
            subtitle: '节点运行时统一视图（JDK / Node.js），可扫描常见安装路径发现候选',
            scan: '扫描发现',
            scanTitle: '扫描发现运行时',
            scanning: '正在扫描节点常见安装路径…',
            scanEmpty: '未发现运行时候选',
            alreadyRegistered: '已在库',
            register: '入库所选（{{count}}）',
            empty: '尚无运行时',
            installNode: '安装 Node.js',
            installTitle: '安装 Node.js',
            installMajor: '主版本',
            installHint: '从 nodejs.org dist（或平台设置的镜像源）下载该主版本最新便携归档。',
            installConfirm: '下发安装',
            deleteRecordTitle: '删除运行时登记记录?',
            deleteRecordDesc: '外部登记的运行时仅删除平台记录，不影响磁盘上的文件。',
            deleteManagedTitle: '删除托管运行时及其文件?',
            deleteManagedDesc: '这是平台一键安装的托管运行时，删除将同时清除节点上的整个安装目录，不可恢复。',
          },
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

beforeAll(() => {
  // Radix Dialog 依赖 ResizeObserver；jsdom 未必提供。
  globalThis.ResizeObserver ??= class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
})

type SectionProps = Parameters<typeof NodeRuntimeSection>[0]

function renderSection(props: Partial<SectionProps> = {}) {
  const handlers: SectionProps = {
    onScan: vi.fn().mockResolvedValue([]),
    onRegister: vi.fn().mockResolvedValue(0),
    onDelete: vi.fn().mockResolvedValue(true),
    onInstall: vi.fn().mockResolvedValue(true),
    ...props,
  }
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  const result = render(<NodeRuntimeSection {...handlers} />, { wrapper: Wrapper })
  return { ...handlers, ...result }
}

/** 含一条 jdk 与一条 nodejs：前者必须被列表过滤掉（见组件内注释的双列重复修复）。 */
const runtimes: RuntimeRowView[] = [
  { id: 1, type: 'jdk', name: 'temurin-21', version: '21.0.2', arch: 'amd64', majorVersion: 21, path: '/opt/jdks/temurin-21', managed: true },
  { id: 2, type: 'nodejs', name: 'Node.js 18', version: '18.20.0', arch: 'amd64', majorVersion: 18, path: '/usr/local/bin/node', managed: true },
]

const candidates: RuntimeCandidateView[] = [
  { type: 'jdk', vendor: 'temurin', majorVersion: 21, version: '21.0.2', arch: 'amd64', path: '/opt/jdks/temurin-21', alreadyRegistered: true },
  { type: 'nodejs', vendor: 'nodejs', majorVersion: 22, version: '22.11.0', arch: 'amd64', path: '/usr/local/bin/node', alreadyRegistered: false },
]

describe('NodeRuntimeSection（FR-298 · ADR-097 b 范式）', () => {
  it('列表只承载非 JDK 类型，扫描发现后勾选候选上报入库', async () => {
    const user = userEvent.setup()
    // 自带 mock 而非取 renderSection 的返回值：后者是可被 props 覆盖的联合类型，
    // 拿它断言 `.mock.calls` 既不类型安全、也容易断到默认 mock 上（详见先前 307 测试的教训）。
    const onRegister = vi.fn<(picked: RuntimeCandidateView[]) => Promise<number>>().mockResolvedValue(1)
    const { onScan } = renderSection({
      runtimes,
      onScan: vi.fn().mockResolvedValue(candidates),
      onRegister,
    })

    // 分区列表不再重复列 type=jdk（v0.15.0 验收 e2e 抓出的整页双列重复修复）。
    expect(screen.getByText('Node.js 18')).toBeInTheDocument()
    expect(screen.queryByText('/opt/jdks/temurin-21')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /扫描发现/ }))
    expect(onScan).toHaveBeenCalledTimes(1)
    expect(await screen.findByText('扫描发现运行时')).toBeInTheDocument()

    // jdk 候选与已登记路径相同 → 标「已在库」且禁勾；nodejs 候选可勾。
    expect(await screen.findByText('已在库')).toBeInTheDocument()
    const nodeCheckbox = screen.getByRole('checkbox', { name: '/usr/local/bin/node' })
    expect(nodeCheckbox).toBeEnabled()
    expect(screen.getByRole('checkbox', { name: '/opt/jdks/temurin-21' })).toBeDisabled()

    // 未勾选时入库按钮禁用；勾选 nodejs 候选后入库，且只上报被勾中的那一条。
    expect(screen.getByRole('button', { name: /入库所选/ })).toBeDisabled()
    await user.click(nodeCheckbox)
    await user.click(screen.getByRole('button', { name: /入库所选/ }))

    await waitFor(() => expect(onRegister).toHaveBeenCalledTimes(1))
    expect(onRegister.mock.calls[0][0].map((c) => c.path)).toEqual(['/usr/local/bin/node'])
    // 上报成功 1 条 → 视图关闭模态（列表由外壳刷新，不在此断言）。
    await waitFor(() => expect(screen.queryByText('扫描发现运行时')).not.toBeInTheDocument())
  })

  it('安装 Node.js：LTS 快选切换后上报所选主版本，成功后关闭模态', async () => {
    const user = userEvent.setup()
    const { onInstall } = renderSection()

    await user.click(screen.getByRole('button', { name: /安装 Node\.js/ }))
    expect(await screen.findByText('主版本')).toBeInTheDocument()

    // 默认 22；切到 20 LTS 后下发。
    await user.click(screen.getByRole('button', { name: '20 LTS' }))
    await user.click(screen.getByRole('button', { name: /下发安装/ }))

    await waitFor(() => expect(onInstall).toHaveBeenCalledWith(20))
    await waitFor(() => expect(screen.queryByRole('button', { name: /下发安装/ })).not.toBeInTheDocument())
  })

  it('安装 Node.js：自定义主版本非法时禁用下发', async () => {
    const user = userEvent.setup()
    renderSection()

    await user.click(screen.getByRole('button', { name: /安装 Node\.js/ }))
    const input = await screen.findByRole('textbox', { name: '主版本' })
    await user.clear(input)
    expect(screen.getByRole('button', { name: /下发安装/ })).toBeDisabled()
    await user.type(input, '24')
    expect(screen.getByRole('button', { name: /下发安装/ })).toBeEnabled()
  })

  it('重扫返回已登记候选时全部禁勾，入库按钮保持禁用', async () => {
    const user = userEvent.setup()
    const allRegistered = candidates.map((c) => ({ ...c, alreadyRegistered: true }))
    const { onScan } = renderSection({
      runtimes,
      // 首次未登记 → 第二次全部已在库（模拟入库后重扫）。
      onScan: vi
        .fn()
        .mockResolvedValueOnce(candidates)
        .mockResolvedValueOnce(allRegistered),
      onRegister: vi.fn().mockResolvedValue(1),
    })

    await user.click(screen.getByRole('button', { name: /扫描发现/ }))
    await user.click(await screen.findByRole('checkbox', { name: '/usr/local/bin/node' }))
    await user.click(screen.getByRole('button', { name: /入库所选/ }))
    await waitFor(() => expect(screen.queryByText('扫描发现运行时')).not.toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: /扫描发现/ }))
    expect(onScan).toHaveBeenCalledTimes(2)
    await waitFor(() => expect(screen.getAllByText('已在库')).toHaveLength(2))
    expect(screen.getByRole('checkbox', { name: '/usr/local/bin/node' })).toBeDisabled()
    expect(screen.getByRole('button', { name: /入库所选/ })).toBeDisabled()
  })

  it('删除托管项走 DangerConfirm：确认前需逐字输入资源名', async () => {
    const user = userEvent.setup()
    const onDelete = vi.fn<(item: RuntimeRowView) => Promise<boolean>>().mockResolvedValue(true)
    renderSection({ runtimes, onDelete })

    await user.click(screen.getByRole('button', { name: '删除' }))
    expect(await screen.findByText('删除托管运行时及其文件?')).toBeInTheDocument()
    // 按钮名与行内删除按钮同名（都是「删除」）：取弹窗内的那个。
    const dialog = document.querySelector('[role="dialog"]')!
    const confirm = Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent?.includes('删除'))!
    // 托管项要求逐字输入资源名才能确认（confirmText 门禁）：未输入时确认按钮必须禁用。
    expect(confirm).toBeDisabled()
    await user.type(dialog.querySelector('input')!, 'Node.js 18')
    await waitFor(() => expect(confirm).toBeEnabled())
    await user.click(confirm)

    await waitFor(() => expect(onDelete).toHaveBeenCalledTimes(1))
    expect(onDelete.mock.calls[0][0]).toMatchObject({ id: 2, type: 'nodejs' })
  })
})
