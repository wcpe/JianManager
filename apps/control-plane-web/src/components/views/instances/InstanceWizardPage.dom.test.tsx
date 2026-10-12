import { beforeAll, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { InstanceWizardPage } from '@/components/views/instances/InstanceWizardPage'
import type { InstanceWizardPageProps } from '@/components/views/instances/InstanceWizardPage'

/**
 * FR-230 创建实例向导 · 受控视图测（ADR-102 a 范式）。
 *
 * 应用侧原本没有向导的行为测试；这里补的是**分步语义与上报契约**：
 * 节点变更上报（驱动外壳取 JDK/Docker）、分步阻断、docker 检测不可用阻断提交、
 * 创建载荷形态（含 docker 专属字段与环境变量收敛）、成功/失败提示通道。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        common: {
          cancel: '取消', back: '上一步', create: '创建', creating: '创建中...',
          enabled: '启用', disabled: '关闭', delete: '删除', type: '通用',
        },
        nav: { allInstances: '全部实例' },
        nodes: { online: '在线', offline: '离线', starting: '启动中', maintenance: '维护中' },
        templates: { selectTemplate: '选择模板（可选）', noTemplate: '不使用模板' },
        instances: {
          createInstance: '创建实例', wizardSubtitle: '按步骤填写', wizardNext: '下一步',
          wizardStep: { basic: '基本', launch: '启动', advanced: '高级', review: '确认' },
          wizardHint: { basic: '先起个名字', launch: '怎么启动', advanced: '进阶选项', review: '看一眼' },
          instanceName: '实例名称', node: '节点', type: '类型', group: '分组',
          selectNode: '选择节点', noNodesHint: '还没有节点', noGroup: '不分组',
          jdkOptional: 'JDK（可选）', jdkSystemDefault: '不指定（使用系统 Java）',
          startCommandHint: '启动命令提示', startCommandDockerHint: 'docker 提示',
          cpuLimit: 'CPU 限额', memLimit: '内存限额', diskLimit: '磁盘限额',
          diskLimitHint: '磁盘提示', resourceLimitHint: '资源限额提示',
          dockerImage: 'Docker 镜像', dockerEnvVars: '环境变量', dockerEnvEmpty: '暂无',
          dockerEnvKey: '变量名', dockerEnvValue: '变量值', dockerEnvAdd: '添加变量',
          dockerChecking: '正在检测 Docker', dockerAvailable: 'Docker 可用：{{version}}',
          dockerUnavailable: 'Docker 不可用', dockerMcPresetTitle: '一键 Minecraft',
          dockerMcPresetHint: '镜像预设', dockerMcPresetApply: '应用预设',
          unlimited: '不限', created: '实例已创建', createFailed: '创建实例失败',
        },
        instanceDetail: { processType: '启动方式', startCommand: '启动命令', autoRestart: '崩溃自动重启' },
        validation: { requiredMark: '必填' },
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

function renderWizard(over: Partial<InstanceWizardPageProps> = {}) {
  const props: InstanceWizardPageProps = {
    initialNodeId: '',
    initialTemplateId: '',
    nodes: [{ id: 1, name: 'n1', status: 1 }],
    nodeStatusLabels: { online: '在线', offline: '离线', starting: '启动中', maintenance: '维护中' },
    hasNoNodes: false,
    jdkOptions: [],
    groupOptions: [],
    templates: [],
    onNodeChange: vi.fn(),
    dockerCheck: { fetching: false, blocked: false },
    onCreate: vi.fn().mockResolvedValue(undefined),
    onCancel: vi.fn(),
    notify: vi.fn(),
    ...over,
  }
  const view = render(
    <I18nextProvider i18n={testI18n}>
      <InstanceWizardPage {...props} />
    </I18nextProvider> as ReactNode,
  )
  return { props, view }
}

describe('InstanceWizardPage（FR-230 创建实例向导）', () => {
  it('基本步缺名称/节点时「下一步」禁用，补齐后可进入启动步', async () => {
    const user = userEvent.setup()
    const { props } = renderWizard()
    const next = screen.getByRole('button', { name: /下一步/ })
    expect(next).toBeDisabled()

    await user.type(screen.getByPlaceholderText('Survival Server'), '新服')
    expect(next).toBeDisabled() // 仍未选节点

    await user.click(screen.getByText('选择节点'))
    await user.click(await screen.findByText(/n1（在线）/))
    await waitFor(() => expect(props.onNodeChange).toHaveBeenCalledWith('1'))
    await waitFor(() => expect(next).toBeEnabled())

    await user.click(next)
    expect(screen.getByText('启动方式')).toBeInTheDocument()
  })

  it('选 docker 启动方式后出现「高级」步，检测不可用时阻断继续', async () => {
    const user = userEvent.setup()
    const { props } = renderWizard({
      initialNodeId: '1',
      dockerCheck: { fetching: false, available: false, error: '未装 docker', blocked: true },
      onProcessTypeChange: vi.fn(),
    })
    // 基本步已有节点与名称
    await user.type(screen.getByPlaceholderText('Survival Server'), '新服')
    await user.click(screen.getByRole('button', { name: /下一步/ }))

    // 启动步：切到 docker
    await user.click(screen.getByText('daemon (启用)'))
    await user.click(await screen.findByText('docker'))
    await waitFor(() => expect(props.onProcessTypeChange).toHaveBeenCalledWith('docker'))

    // 启动命令非必填 → 可进入高级步
    await user.click(screen.getByRole('button', { name: /下一步/ }))
    expect(await screen.findByText(/Docker 不可用/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /下一步/ })).toBeDisabled()
  })

  it('确认步回显已填内容，提交按非 docker 形态收敛载荷并提示成功', async () => {
    const user = userEvent.setup()
    const onCreate = vi.fn().mockResolvedValue(undefined)
    const notify = vi.fn()
    const onCancel = vi.fn()
    const { props } = renderWizard({ initialNodeId: '1', onCreate, notify, onCancel })

    await user.type(screen.getByPlaceholderText('Survival Server'), '新服')
    await user.click(screen.getByRole('button', { name: /下一步/ })) // → 启动
    await user.click(screen.getByRole('button', { name: /下一步/ })) // → 确认

    expect(screen.getByText('新服')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '创建' }))

    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1))
    expect(onCreate.mock.calls[0][0]).toMatchObject({
      nodeId: 1,
      name: '新服',
      processType: 'daemon',
      autoRestart: true,
    })
    // 非 docker：镜像与限额字段为 undefined（键存在但无值，等价于不下发）
    expect(onCreate.mock.calls[0][0].image).toBeUndefined()
    expect(onCreate.mock.calls[0][0].cpuLimit).toBeUndefined()
    expect(onCreate.mock.calls[0][0].envVars).toBeUndefined()
    await waitFor(() => expect(notify).toHaveBeenCalledWith('success', '实例已创建'))
    expect(onCancel).toHaveBeenCalled()
    expect(props.onNodeChange).toBeDefined()
  })

  it('创建失败时优先透出服务端 message，且不离开向导', async () => {
    const user = userEvent.setup()
    const onCreate = vi.fn().mockRejectedValue({ response: { data: { message: '节点容量不足' } } })
    const notify = vi.fn()
    const onCancel = vi.fn()
    renderWizard({ initialNodeId: '1', onCreate, notify, onCancel })

    await user.type(screen.getByPlaceholderText('Survival Server'), '新服')
    await user.click(screen.getByRole('button', { name: /下一步/ }))
    await user.click(screen.getByRole('button', { name: /下一步/ }))
    await user.click(screen.getByRole('button', { name: '创建' }))

    await waitFor(() => expect(notify).toHaveBeenCalledWith('error', '节点容量不足'))
    expect(onCancel).not.toHaveBeenCalled()
  })

  /**
   * `Field` → `FieldLabel` 必须真正与输入框关联（`htmlFor`/`id`）。
   *
   * `FieldLabel` 渲染的是 `<label>`，但没有 `htmlFor` 时它不与任何控件关联——本页原状即如此
   * （标签与输入框只是兄弟节点），于是每个文本输入框在读屏里都没有名字。
   */
  it('文本输入框有可访问名（标签与控件真正关联）', async () => {
    const user = userEvent.setup()
    renderWizard({ initialNodeId: '1' })

    // 基本步：实例名称。
    const nameInput = screen.getByLabelText(/实例名称/)
    expect(nameInput).toHaveAttribute('placeholder', 'Survival Server')
    await user.type(nameInput, '新服')
    expect(nameInput).toHaveValue('新服')

    // 启动步：启动命令（非 docker 时必填，同样走 Field）。
    await user.click(screen.getByRole('button', { name: /下一步/ }))
    const cmdInput = screen.getByLabelText(/启动命令/)
    expect(cmdInput).toHaveAttribute('placeholder', 'java -Xmx2G -jar server.jar nogui')
    expect(await screen.findByText('怎么启动')).toBeInTheDocument()
  })

  /** 步骤指示器要标出「当前步骤」（原先只靠底色与字重表达，读屏听不出来）。 */
  it('步骤指示器用 aria-current="step" 标出当前步骤', async () => {
    const user = userEvent.setup()
    renderWizard({ initialNodeId: '1' })

    const steps = screen.getAllByRole('listitem')
    expect(steps[0]).toHaveAttribute('aria-current', 'step')
    expect(steps[1]).not.toHaveAttribute('aria-current')

    // 推进到启动步后，当前标记随之移动。
    await user.type(screen.getByPlaceholderText('Survival Server'), '新服')
    await user.click(screen.getByRole('button', { name: /下一步/ }))
    const after = screen.getAllByRole('listitem')
    expect(after[1]).toHaveAttribute('aria-current', 'step')
    expect(after[0]).not.toHaveAttribute('aria-current')
  })

  /**
   * docker 环境变量行是可编辑行，key 必须稳定。
   *
   * 用下标当 key 时，删掉中间一行会让其后每行的 DOM 身份前移一格：正在编辑的输入框
   * （焦点、光标位置、输入法组合态）会落到下一条数据上——用户删一行，结果改了别人的内容。
   */
  it('docker 环境变量行：删中间行后其余行的 DOM 身份不变', async () => {
    const user = userEvent.setup()
    renderWizard({
      initialNodeId: '1',
      dockerCheck: { fetching: false, available: true, version: '24.0', blocked: false },
      onProcessTypeChange: vi.fn(),
    })

    await user.type(screen.getByPlaceholderText('Survival Server'), '新服')
    await user.click(screen.getByRole('button', { name: /下一步/ }))
    await user.click(screen.getByText('daemon (启用)'))
    await user.click(await screen.findByText('docker'))
    await user.click(screen.getByRole('button', { name: /下一步/ }))

    // 一键预设（EULA=TRUE）+ 再手动加两行 → 共 3 行。
    await user.click(await screen.findByRole('button', { name: /应用预设/ }))
    await user.click(screen.getByRole('button', { name: '添加变量' }))
    await user.click(screen.getByRole('button', { name: '添加变量' }))

    const keysBefore = screen.getAllByLabelText('变量名')
    expect(keysBefore).toHaveLength(3)
    const lastBefore = keysBefore[2]
    const thirdValueBefore = screen.getAllByLabelText('变量值')[2]

    // 删中间那行。
    await user.click(screen.getAllByRole('button', { name: '删除' })[1])

    const keysAfter = screen.getAllByLabelText('变量名')
    expect(keysAfter).toHaveLength(2)
    // 关键：原来的第 3 行仍是同一个 DOM 节点（下标 key 会让它变成新的第 2 行节点）。
    expect(keysAfter[1]).toBe(lastBefore)
    expect(screen.getAllByLabelText('变量值')[1]).toBe(thirdValueBefore)
  })
})
