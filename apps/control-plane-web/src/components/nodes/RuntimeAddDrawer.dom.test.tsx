import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import '@/i18n'
import { RuntimeAddDrawer } from './RuntimeAddDrawer'

/**
 * 抽屉分段与运行时类型的对应关系。
 *
 * 【这组用例针对一个真实故障】改造前抽屉只有 install/register/scan 三段，
 * 「安装」段**无条件**渲染 JDK 面板，而 Node.js 的安装入口被塞在「扫描发现」里面。
 * 用户的实际观感就是：点「安装 Node.js」打开的是装 JDK。
 *
 * 所以这里断言的不是"某段有内容"，而是**分段标签与内容必须对应**：
 * 点「安装 Node.js」看到的是 Node 版本下拉，不是 JDK 的厂商/大版本表单。
 */
const state = { renderJdk: 0, renderNodeInstall: 0 }

vi.mock('@/components/nodes/NodeJDKTab', () => ({
  default: () => {
    state.renderJdk += 1
    return <div data-testid="jdk-panel">JDK 面板</div>
  },
}))
vi.mock('@/components/nodes/NodeInstallSection', () => ({
  NodeInstallSection: () => {
    state.renderNodeInstall += 1
    return <div data-testid="node-install-section">Node 安装段</div>
  },
}))
vi.mock('@/components/nodes/NodeRegisterSection', () => ({
  NodeRegisterSection: ({ type }: { type: string }) => (
    <div data-testid="node-register-section">Node 登记段 · {type}</div>
  ),
}))
vi.mock('@/components/views/nodes/NodeRuntimeSection', () => ({
  default: () => <div data-testid="runtime-scan-section">扫描段</div>,
}))
vi.mock('@/api/runtimes', () => ({
  useScanRuntimes: () => ({ mutateAsync: vi.fn() }),
  useRegisterRuntime: () => ({ mutateAsync: vi.fn() }),
  useInstallRuntime: () => ({ mutateAsync: vi.fn(), isPending: false }),
}))
vi.mock('@/components/nodes/DirectoryPicker', () => ({ default: () => null }))
vi.mock('@/components/nodes/PingNodeButton', () => ({ PingNodeButton: () => null }))
vi.mock('@/api/nodeRuntime', () => ({ useJDKCatalog: () => ({ data: undefined }) }))
vi.mock('@/api/jdks', () => ({
  useNodeJDKs: () => ({ data: [], isLoading: false }),
  useCreateJDK: () => ({ mutateAsync: vi.fn() }),
  useDeleteJDK: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useInstallJDK: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useProbeJDK: () => ({ mutateAsync: vi.fn() }),
  useUpdateJDK: () => ({ mutateAsync: vi.fn() }),
}))

function setup(tab: 'jdk' | 'node' | 'register' | 'register-node' | 'scan') {
  const onTab = vi.fn()
  render(
    <RuntimeAddDrawer
      open
      onOpenChange={vi.fn()}
      nodeId={1}
      online
      tab={tab}
      onTab={onTab}
      editing={null}
      onCloseEdit={vi.fn()}
      onSaveEdit={async () => true}
      pendingDelete={null}
      onCloseDelete={vi.fn()}
      onConfirmDelete={vi.fn()}
    />,
  )
  return { onTab }
}

beforeEach(() => {
  state.renderJdk = 0
  state.renderNodeInstall = 0
})

describe('RuntimeAddDrawer 分段与内容对应', () => {
  it('有独立的「安装 Node.js」分段，不再是「安装」一个含糊的段', () => {
    setup('jdk')
    expect(screen.getByTestId('runtime-add-jdk')).toBeInTheDocument()
    expect(screen.getByTestId('runtime-add-node')).toBeInTheDocument()
    // 旧的分段 id 不该再出现（它意味着"安装"没有区分类型）。
    expect(screen.queryByTestId('runtime-add-install')).not.toBeInTheDocument()
  })

  it('停在 JDK 段时渲染 JDK 面板，不渲染 Node 安装段', () => {
    setup('jdk')
    expect(screen.getByTestId('jdk-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('node-install-section')).not.toBeInTheDocument()
  })

  it('停在 Node 段时渲染 Node 安装段，**不渲染 JDK 面板**', () => {
    // 这条就是那个故障的回归防线：点 Node 却看到 JDK，这条会红。
    setup('node')
    expect(screen.getByTestId('node-install-section')).toBeInTheDocument()
    expect(screen.queryByTestId('jdk-panel')).not.toBeInTheDocument()
  })

  it('点「安装 Node.js」分段上报 node', async () => {
    const { onTab } = setup('jdk')
    await userEvent.click(screen.getByTestId('runtime-add-node'))
    expect(onTab).toHaveBeenCalledWith('node')
  })

  it('登记段仍复用 JDK 面板（登记的是已是 JDK 的文件）', () => {
    setup('register')
    expect(screen.getByTestId('jdk-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('node-install-section')).not.toBeInTheDocument()
  })

  /*
    JDK 有「登记已有 JDK」，Node.js 此前**没有**登记入口——只能下载安装或扫描发现。
    用户把 Node 装在 /opt/node-v22 这类非标准位置时，两条路都走不通。
    后端 POST /runtimes 本来就支持 type=nodejs 登记，缺的只是这个入口。
  */
  it('有独立的「登记已有 Node.js」分段', () => {
    setup('jdk')
    expect(screen.getByTestId('runtime-add-register-node')).toBeInTheDocument()
  })

  it('停在「登记已有 Node.js」段时渲染 Node 登记段，**不渲染 JDK 面板**', () => {
    setup('register-node')
    expect(screen.getByTestId('node-register-section')).toBeInTheDocument()
    expect(screen.queryByTestId('jdk-panel')).not.toBeInTheDocument()
    expect(screen.queryByTestId('node-install-section')).not.toBeInTheDocument()
  })

  it('点「登记已有 Node.js」分段上报 register-node', async () => {
    const { onTab } = setup('jdk')
    await userEvent.click(screen.getByTestId('runtime-add-register-node'))
    expect(onTab).toHaveBeenCalledWith('register-node')
  })

  it('编辑 JDK A 取消后再编辑 JDK B 不会串入 JDK A 的残留草稿，且非 JDK 行不会弹出 JDK 编辑框', async () => {
    const jdkA = {
      key: 'jdk-1', id: 1, type: 'jdk', name: 'Temurin', version: '21.0.4', majorVersion: 21,
      arch: 'x64', path: '/opt/jdk-21', managed: true, refCount: 0, refs: [], deleteBlocked: false, deleteBlockedReason: '',
    }
    const jdkB = {
      key: 'jdk-2', id: 2, type: 'jdk', name: 'Zulu', version: '17.0.9', majorVersion: 17,
      arch: 'x64', path: '/opt/jdk-17', managed: false, refCount: 0, refs: [], deleteBlocked: false, deleteBlockedReason: '',
    }
    const nodeRow = {
      key: 'nodejs-1', id: 1, type: 'nodejs', name: 'Node.js 22', version: '22.17.0', majorVersion: 22,
      arch: 'x64', path: '/opt/node-22/bin/node', managed: true, refCount: 0, refs: [], deleteBlocked: false, deleteBlockedReason: '',
    }
    const onSaveEdit = vi.fn(async () => true)
    const { rerender } = render(
      <RuntimeAddDrawer
        open={false}
        onOpenChange={vi.fn()}
        nodeId={1}
        online
        tab="jdk"
        onTab={vi.fn()}
        editing={jdkA}
        onCloseEdit={vi.fn()}
        onSaveEdit={onSaveEdit}
        pendingDelete={null}
        onCloseDelete={vi.fn()}
        onConfirmDelete={vi.fn()}
      />,
    )

    const vendorInput = screen.getByDisplayValue('Temurin')
    await userEvent.clear(vendorInput)
    await userEvent.type(vendorInput, 'Corretto')

    // 受控关闭后再打开 JDK B，必须展示 JDK B 自身字段，不能带出 Corretto。
    rerender(
      <RuntimeAddDrawer
        open={false}
        onOpenChange={vi.fn()}
        nodeId={1}
        online
        tab="jdk"
        onTab={vi.fn()}
        editing={null}
        onCloseEdit={vi.fn()}
        onSaveEdit={onSaveEdit}
        pendingDelete={null}
        onCloseDelete={vi.fn()}
        onConfirmDelete={vi.fn()}
      />,
    )
    rerender(
      <RuntimeAddDrawer
        open={false}
        onOpenChange={vi.fn()}
        nodeId={1}
        online
        tab="jdk"
        onTab={vi.fn()}
        editing={jdkB}
        onCloseEdit={vi.fn()}
        onSaveEdit={onSaveEdit}
        pendingDelete={null}
        onCloseDelete={vi.fn()}
        onConfirmDelete={vi.fn()}
      />,
    )
    expect(screen.getByDisplayValue('Zulu')).toBeInTheDocument()
    expect(screen.queryByDisplayValue('Corretto')).not.toBeInTheDocument()

    // 即使上层误传非 JDK 行，也不得打开 JDK 编辑框。
    rerender(
      <RuntimeAddDrawer
        open={false}
        onOpenChange={vi.fn()}
        nodeId={1}
        online
        tab="jdk"
        onTab={vi.fn()}
        editing={nodeRow}
        onCloseEdit={vi.fn()}
        onSaveEdit={onSaveEdit}
        pendingDelete={null}
        onCloseDelete={vi.fn()}
        onConfirmDelete={vi.fn()}
      />,
    )
    expect(screen.queryByText('编辑 JDK 登记信息')).not.toBeInTheDocument()
  })
})
