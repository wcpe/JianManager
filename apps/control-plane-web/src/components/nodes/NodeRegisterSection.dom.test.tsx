import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import '@/i18n'
import { NodeRegisterSection } from './NodeRegisterSection'

/**
 * 「使用本机已有运行时」登记段。
 *
 * 【这组用例守的是什么】用户把运行时装在 /opt/node-v22 这种非标准位置时，
 * 扫描发现扫不到、又没有登记入口，等于这台机器上的运行时永远进不了平台。
 * 后端 POST /runtimes 一直支持 type=nodejs 登记，缺的是前端入口与探测回显。
 *
 * 【断言的契约】探测失败是**常规输入结果**而不是请求异常：必须内联显示在输入框下，
 * 不能弹 toast，也不能清空用户已输入的路径（否则用户要重打一遍）。
 */
const probeState: {
  mutate: ReturnType<typeof vi.fn>
} = { mutate: vi.fn() }
let probePending = false

vi.mock('@/api/runtimes', () => ({
  useProbeRuntime: () => ({
    mutate: probeState.mutate,
    isPending: probePending,
  }),
}))
vi.mock('@/components/nodes/DirectoryPicker', () => ({
  default: ({ onPick, onCancel }: { onPick: (p: string) => void; onCancel: () => void }) => (
    <div data-testid="dir-picker">
      <button type="button" onClick={() => onPick('/opt/node-v22')}>选这个目录</button>
      <button type="button" onClick={onCancel}>取消</button>
    </div>
  ),
}))

function setup(over: Partial<Parameters<typeof NodeRegisterSection>[0]> = {}) {
  const onRegister = vi.fn(async () => true)
  render(
    <NodeRegisterSection nodeId={1} type="nodejs" online onRegister={onRegister} {...over} />,
  )
  return { onRegister }
}

/** 模拟一次成功的探测回调（组件通过 mutate 的 options 拿结果）。 */
function resolveProbe(result: unknown) {
  const opts = probeState.mutate.mock.calls.at(-1)?.[1] as { onSuccess?: (r: unknown) => void }
  opts?.onSuccess?.(result)
}

const okCandidate = {
  type: 'nodejs',
  vendor: 'Node.js',
  version: '22.11.0',
  majorVersion: 22,
  arch: 'x64',
  path: '/opt/node-v22',
  alreadyRegistered: false,
}

beforeEach(() => {
  probeState.mutate = vi.fn()
  probePending = false
})

describe('NodeRegisterSection 登记已有运行时', () => {
  it('默认不显示「确认登记」按钮为可用状态（还没探测）', () => {
    setup()
    expect(screen.getByTestId('node-register-submit-nodejs')).toBeDisabled()
  })

  it('探测成功 → 回显厂商/版本/架构/路径，且可以提交', async () => {
    setup()
    await userEvent.type(screen.getByTestId('node-register-path-nodejs'), '/opt/node-v22')
    await userEvent.click(screen.getByTestId('node-register-probe-nodejs'))
    resolveProbe({ valid: true, candidate: okCandidate })

    await waitFor(() => expect(screen.getByTestId('node-register-result-nodejs')).toBeInTheDocument())
    const box = screen.getByTestId('node-register-result-nodejs')
    expect(box).toHaveTextContent('22.11.0')
    expect(box).toHaveTextContent('x64')
    expect(box).toHaveTextContent('/opt/node-v22')
    expect(screen.getByTestId('node-register-submit-nodejs')).toBeEnabled()
  })

  it('探测失败 → 内联错误、不弹窗、不清空输入、不可提交', async () => {
    setup()
    const input = screen.getByTestId('node-register-path-nodejs')
    await userEvent.type(input, '/opt/not-node')
    await userEvent.click(screen.getByTestId('node-register-probe-nodejs'))
    resolveProbe({ valid: false, error: '该路径下没有找到 Node.js 可执行文件' })

    await waitFor(() =>
      expect(screen.getByTestId('node-register-error-nodejs')).toHaveTextContent('没有找到 Node.js'),
    )
    // 用户输的路径必须还在——清空等于让用户重打。
    expect(input).toHaveValue('/opt/not-node')
    expect(screen.getByTestId('node-register-submit-nodejs')).toBeDisabled()
  })

  it('提交时带上探测出的字段，且 managed=false（外部登记不归托管根管）', async () => {
    const { onRegister } = setup()
    await userEvent.type(screen.getByTestId('node-register-path-nodejs'), '/opt/node-v22')
    await userEvent.click(screen.getByTestId('node-register-probe-nodejs'))
    resolveProbe({ valid: true, candidate: okCandidate })
    await waitFor(() => expect(screen.getByTestId('node-register-submit-nodejs')).toBeEnabled())

    await userEvent.click(screen.getByTestId('node-register-submit-nodejs'))

    expect(onRegister).toHaveBeenCalledTimes(1)
    expect(onRegister).toHaveBeenCalledWith(
      expect.objectContaining({
        type: 'nodejs',
        majorVersion: 22,
        version: '22.11.0',
        arch: 'x64',
        path: '/opt/node-v22',
        managed: false,
        name: 'Node.js 22',
      }),
    )
  })

  it('已登记的路径 → 显示「已登记」徽章且不可重复提交', async () => {
    setup()
    await userEvent.type(screen.getByTestId('node-register-path-nodejs'), '/opt/node-v22')
    await userEvent.click(screen.getByTestId('node-register-probe-nodejs'))
    resolveProbe({ valid: true, candidate: { ...okCandidate, alreadyRegistered: true } })

    await waitFor(() => expect(screen.getByTestId('node-register-already-nodejs')).toBeInTheDocument())
    expect(screen.getByTestId('node-register-submit-nodejs')).toBeDisabled()
  })

  it('改路径后旧的探测结果失效（否则会让人以为新路径已验证）', async () => {
    setup()
    const input = screen.getByTestId('node-register-path-nodejs')
    await userEvent.type(input, '/opt/node-v22')
    await userEvent.click(screen.getByTestId('node-register-probe-nodejs'))
    resolveProbe({ valid: true, candidate: okCandidate })
    await waitFor(() => expect(screen.getByTestId('node-register-result-nodejs')).toBeInTheDocument())

    await userEvent.type(input, '2')
    expect(screen.queryByTestId('node-register-result-nodejs')).not.toBeInTheDocument()
    expect(screen.getByTestId('node-register-submit-nodejs')).toBeDisabled()
  })

  it('目录选择器：选完自动回填并立即探测', async () => {
    setup()
    await userEvent.click(screen.getByTestId('node-register-browse-nodejs'))
    await userEvent.click(screen.getByText('选这个目录'))

    expect(screen.getByTestId('node-register-path-nodejs')).toHaveValue('/opt/node-v22')
    // 选完即探测：用户从列表点出来的路径，几乎总想马上确认。
    expect(probeState.mutate).toHaveBeenCalledWith(
      { type: 'nodejs', path: '/opt/node-v22' },
      expect.anything(),
    )
  })

  it('节点离线 → 探测/浏览/提交全禁用，并给出原因', () => {
    setup({ online: false })
    expect(screen.getByRole('alert')).toHaveTextContent('节点离线')
    expect(screen.getByTestId('node-register-probe-nodejs')).toBeDisabled()
    expect(screen.getByTestId('node-register-browse-nodejs')).toBeDisabled()
    expect(screen.getByTestId('node-register-submit-nodejs')).toBeDisabled()
  })

  it('JDK 与 Node.js 共用同一套流程（type=jdk 时用 vendor 而非 name）', async () => {
    const onRegister = vi.fn(async () => true)
    render(<NodeRegisterSection nodeId={1} type="jdk" online onRegister={onRegister} />)
    await userEvent.type(screen.getByTestId('node-register-path-jdk'), '/opt/jdk-21')
    await userEvent.click(screen.getByTestId('node-register-probe-jdk'))
    resolveProbe({
      valid: true,
      candidate: { ...okCandidate, type: 'jdk', vendor: 'Temurin', majorVersion: 21, path: '/opt/jdk-21' },
    })
    await waitFor(() => expect(screen.getByTestId('node-register-submit-jdk')).toBeEnabled())
    await userEvent.click(screen.getByTestId('node-register-submit-jdk'))

    expect(onRegister).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'jdk', vendor: 'Temurin', managed: false }),
    )
    // jdk 不带 name（展示名用厂商）。
    expect((onRegister.mock.calls[0]?.[0] as { name?: string }).name).toBeUndefined()
  })
})
