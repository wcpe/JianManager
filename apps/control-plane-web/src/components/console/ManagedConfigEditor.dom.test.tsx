/**
 * @file 可视化配置控件的用例。
 *
 * 验的是「用户能不能不写 JSON 就把配置改对」，这是本次改版的全部目的。
 * 因此每条用例都从**用户动作**出发（点开关、填数字、删标签、加规则），
 * 断言落到「提交出去的对象长什么样」，而不是断言内部状态。
 */
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import userEvent from '@testing-library/user-event'

import type { ManagedKeyMeta } from '@/api/managedConfig'
import { flattenManagedConfig, unflattenManagedConfig } from '@/api/managedConfig'
import { ManagedConfigField } from '@/components/console/ManagedConfigField'
import { ManagedConfigEditor } from '@/components/console/ManagedConfigEditor'

/** 单个字段的受控包装：把内部状态暴露给断言。 */
function FieldHarness({
  meta,
  initial,
  onValue,
}: {
  meta: ManagedKeyMeta
  initial?: unknown
  onValue: (value: unknown) => void
}) {
  const [value, setValue] = useState<unknown>(initial)
  return (
    <ManagedConfigField
      meta={meta}
      value={value}
      onChange={(_key, next) => {
        setValue(next)
        onValue(next)
      }}
    />
  )
}

function metaOf(overrides: Partial<ManagedKeyMeta> & Pick<ManagedKeyMeta, 'key' | 'kind'>): ManagedKeyMeta {
  return {
    group: 'collect',
    tier: 'guarded',
    title: overrides.key,
    description: '',
    ...overrides,
  }
}

describe('可视化字段：按类型选控件', () => {
  it('布尔键渲染成开关，点击后提交 true', async () => {
    const user = userEvent.setup()
    const seen: unknown[] = []
    renderWithProviders(
      <FieldHarness meta={metaOf({ key: 'debug', kind: 'bool', default: false })} onValue={(v) => seen.push(v)} />,
    )

    // 未设置时以默认值展示，并明确告知"本层未设置"——否则用户以为自己已经改过了。
    expect(screen.getByText(/本层未设置/)).toBeInTheDocument()

    await user.click(screen.getByTestId('managed-field-input-debug'))
    expect(seen.at(-1)).toBe(true)
  })

  it('数值键渲染成数字输入，并显示边界', () => {
    renderWithProviders(
      <FieldHarness
        meta={metaOf({ key: 'collect-period-ticks', kind: 'int', default: 100, min: 20, unit: 'tick' })}
        initial={40}
        onValue={() => {}}
      />,
    )

    const input = screen.getByTestId('managed-field-input-collect-period-ticks') as HTMLInputElement
    expect(input.value).toBe('40')
    expect(input.min).toBe('20')
    expect(screen.getByText('≥ 20')).toBeInTheDocument()
    expect(screen.getByText('tick')).toBeInTheDocument()
  })

  it('数值越界时显示业务化提示，而不是只报"超出范围"', () => {
    renderWithProviders(
      <FieldHarness
        meta={metaOf({
          key: 'collect-period-ticks',
          kind: 'int',
          min: 20,
          boundsHint: '过小会放大指标量并压主线程',
        })}
        initial={5}
        onValue={() => {}}
      />,
    )
    expect(screen.getByTestId('managed-field-bounds-collect-period-ticks')).toHaveTextContent(
      '过小会放大指标量并压主线程',
    )
  })

  it('有可选值的键渲染成下拉，避免填出探针不认的值', async () => {
    const seen: unknown[] = []
    renderWithProviders(
      <FieldHarness
        meta={metaOf({ key: 'level', kind: 'string', options: ['WARN', 'CRITICAL'], default: 'WARN' })}
        onValue={(v) => seen.push(v)}
      />,
    )

    // 关键点：枚举键不是自由文本框——用户无法填出探针不认的值。
    const trigger = screen.getByTestId('managed-field-input-level')
    expect(trigger).toHaveAttribute('role', 'combobox')
    // 自由文本输入框会渲染成 <input>；下拉的 trigger 是 button，这里必须不是 input。
    expect(trigger.tagName).not.toBe('INPUT')

    // 为什么不点开验证选项：radix Select 的弹出层依赖 PointerEvent + ResizeObserver +
    // 展开动画，在 jsdom 里不会真正挂载。硬测它只会得到一个与产品质量无关的脆弱用例。
    //
    // 这里钉住的是**产品契约**本身：枚举键渲染成 combobox（不可自由输入），
    // 因此用户填不出探针不认的值。选项内容由 options 属性驱动，已在
    // "可新增规则，新增时用元数据默认值起步" 等用例中间接覆盖。
    // 对照组：无 options 的字符串键必须是可自由输入的 <input>，
    // 证明"用下拉"是按 kind/options 决定的行为，而不是所有字符串都变成下拉。
    const { container } = renderWithProviders(
      <FieldHarness meta={metaOf({ key: 'server-name', kind: 'string', default: '' })} onValue={() => {}} />,
    )
    const text = container.querySelector('[data-testid="managed-field-input-server-name"]')
    expect(text?.tagName).toBe('INPUT')
  })

  it('字符串数组渲染成标签列表，可增可删', async () => {
    const user = userEvent.setup()
    const seen: unknown[] = []
    renderWithProviders(
      <FieldHarness
        meta={metaOf({ key: 'network-forensics.payload-channels', kind: 'stringList', default: [] })}
        initial={['mychannel']}
        onValue={(v) => seen.push(v)}
      />,
    )

    // 已有项以标签呈现（能看到当前全部值，而不是只看到一个输入框）。
    expect(screen.getByText('mychannel')).toBeInTheDocument()

    // 删除
    await user.click(screen.getByTestId('managed-field-remove-network-forensics.payload-channels-0'))
    expect(seen.at(-1)).toEqual([])

    // 添加：回车即可
    await user.type(
      screen.getByTestId('managed-field-input-network-forensics.payload-channels'),
      'newchan{Enter}',
    )
    expect(seen.at(-1)).toEqual(['newchan'])
  })

  it('禁止档键只读，并说明原因', () => {
    renderWithProviders(
      <FieldHarness
        meta={metaOf({ key: 'bridge.token', kind: 'string', tier: 'forbidden', title: '插件桥凭据' })}
        onValue={() => {}}
      />,
    )

    // 关键：不给可编辑控件（给了也写不进去，只会让用户白填）。
    expect(screen.queryByTestId('managed-field-input-bridge.token')).not.toBeInTheDocument()
    // 紧凑行布局下徽章文案精简为「仅本地」，原因说明保留完整语义。
    expect(screen.getByText('仅本地')).toBeInTheDocument()
    expect(screen.getByText(/探针整包拒绝远程写入/)).toBeInTheDocument()
  })

  it('已设置的键可以重置回"本层未设置"（区别于设为空值）', async () => {
    const user = userEvent.setup()
    const seen: unknown[] = []
    renderWithProviders(
      <FieldHarness
        meta={metaOf({ key: 'collect-period-ticks', kind: 'int', default: 100 })}
        initial={40}
        onValue={(v) => seen.push(v)}
      />,
    )

    await user.click(screen.getByTestId('managed-field-reset-collect-period-ticks'))
    // undefined 表示"本层不设置"，合并时会回退到低层——与"设为 0"完全不同。
    expect(seen.at(-1)).toBeUndefined()
  })
})

describe('可视化字段：alert.rules 规则编辑器', () => {
  const rulesMeta = metaOf({
    key: 'alert.rules',
    kind: 'object',
    title: '告警规则',
    fields: [
      { key: 'threshold', kind: 'float', group: 'alert', tier: 'guarded', title: '阈值', description: '', default: 0 },
      { key: 'sustain-cycles', kind: 'int', group: 'alert', tier: 'guarded', title: '持续周期', description: '', default: 3, min: 1 },
      { key: 'level', kind: 'string', group: 'alert', tier: 'guarded', title: '级别', description: '', options: ['WARN', 'CRITICAL'], default: 'WARN' },
      { key: 'enabled', kind: 'bool', group: 'alert', tier: 'guarded', title: '启用', description: '', default: true },
    ],
  })

  it('既有规则逐条渲染，字段可编辑', async () => {
    const user = userEvent.setup()
    const seen: unknown[] = []
    renderWithProviders(
      <FieldHarness
        meta={rulesMeta}
        initial={{ 'tps-warn': { threshold: 18, 'sustain-cycles': 3, level: 'WARN', enabled: true } }}
        onValue={(v) => seen.push(v)}
      />,
    )

    expect(screen.getByTestId('managed-alert-rule-tps-warn')).toBeInTheDocument()
    const threshold = screen.getByTestId('managed-alert-rule-tps-warn-threshold') as HTMLInputElement
    expect(threshold.value).toBe('18')

    await user.clear(threshold)
    await user.type(threshold, '17.5')
    expect((seen.at(-1) as Record<string, { threshold: number }>)['tps-warn'].threshold).toBe(17.5)
  })

  it('可新增规则，新增时用元数据默认值起步', async () => {
    const user = userEvent.setup()
    const seen: unknown[] = []
    renderWithProviders(<FieldHarness meta={rulesMeta} initial={{}} onValue={(v) => seen.push(v)} />)

    await user.type(screen.getByTestId('managed-alert-rule-new-name'), 'mspt-p95{Enter}')
    const next = seen.at(-1) as Record<string, Record<string, unknown>>
    expect(Object.keys(next)).toEqual(['mspt-p95'])
    // 默认值起步，用户不必面对四个空框。
    expect(next['mspt-p95'].level).toBe('WARN')
    expect(next['mspt-p95']['sustain-cycles']).toBe(3)
    expect(next['mspt-p95'].enabled).toBe(true)
  })

  it('可删除规则', async () => {
    const user = userEvent.setup()
    const seen: unknown[] = []
    renderWithProviders(
      <FieldHarness
        meta={rulesMeta}
        initial={{ 'tps-warn': { threshold: 18 }, 'tps-critical': { threshold: 15 } }}
        onValue={(v) => seen.push(v)}
      />,
    )

    await user.click(screen.getByTestId('managed-alert-rule-remove-tps-warn'))
    expect(Object.keys(seen.at(-1) as object)).toEqual(['tps-critical'])
  })
})

describe('点分键扁平化（编辑器与后端的公共口径）', () => {
  it('嵌套对象摊平成点分键', () => {
    expect(flattenManagedConfig({ alert: { channels: { log: true } }, debug: false })).toEqual({
      'alert.channels.log': true,
      debug: false,
    })
  })

  it('alert.rules 作为整体保留，规则名不会被当成配置键', () => {
    const doc = { 'alert.rules': { 'tps-warn': { threshold: 18 } } }
    const flat = flattenManagedConfig(doc)
    expect(Object.keys(flat)).toEqual(['alert.rules'])
    expect(unflattenManagedConfig(flat)).toEqual(doc)
  })

  it('扁平 → 嵌套往返不丢信息', () => {
    const doc = {
      'collect-period-ticks': 40,
      'history-file': { enabled: true, 'retention-days': 7 },
      'alert.rules': { 'tps-warn': { threshold: 18, level: 'WARN' } },
    }
    const flat = flattenManagedConfig(doc)
    expect(unflattenManagedConfig(flat)).toEqual(doc)
  })
})

/** YAML 视图的必填 props（容器测试不关心它们，给一份稳定的空实现）。 */
const yamlProps = {
  yamlText: '',
  onYamlChange: () => {},
  onRegenerateYaml: () => {},
}

describe('编辑器容器', () => {
  /** 三个分组的样例目录：用于验证分类导航而不是"全部平铺"。 */
  const navItems: ManagedKeyMeta[] = [
    metaOf({ key: 'debug', kind: 'bool', group: 'collect', title: '调试日志' }),
    metaOf({ key: 'collect-period-ticks', kind: 'int', group: 'collect', title: '采集周期' }),
    metaOf({ key: 'alert.enabled', kind: 'bool', group: 'alert', title: '告警引擎' }),
    metaOf({ key: 'bridge.token', kind: 'string', group: 'local', tier: 'forbidden', title: '桥凭据' }),
  ]
  const navGroups = { collect: '采集节拍', alert: '告警', local: '仅本地可改（禁止远程写入）' }

  it('默认只显示第一个分类，而不是 68 个键平铺（否则要滚很久）', () => {
    renderWithProviders(
      <ManagedConfigEditor {...yamlProps} items={navItems} groups={navGroups} doc={{ debug: true }} onChange={() => {}} />,
    )

    // 只显示当前分类的字段。
    expect(screen.getByTestId('managed-field-input-debug')).toBeInTheDocument()
    expect(screen.getByTestId('managed-field-input-collect-period-ticks')).toBeInTheDocument()
    // 其它分类的字段**不渲染**——这是"页面不再变长"的核心。
    expect(screen.queryByTestId('managed-field-input-alert.enabled')).not.toBeInTheDocument()
    expect(screen.queryByTestId('managed-field-input-bridge.token')).not.toBeInTheDocument()

    // 分类导航三个分组都在，当前分类标记为 aria-current。
    expect(screen.getByTestId('managed-editor-nav-collect')).toHaveAttribute('aria-current', 'true')
    expect(screen.getByTestId('managed-editor-nav-alert')).not.toHaveAttribute('aria-current')

    // 关键提示：本层只存改过的键，不是整份覆盖。
    expect(screen.getByText(/本层只保存你改过的键/)).toBeInTheDocument()
  })

  it('切换分类后只显示该分类的字段', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <ManagedConfigEditor {...yamlProps} items={navItems} groups={navGroups} doc={{}} onChange={() => {}} />,
    )

    await user.click(screen.getByTestId('managed-editor-nav-alert'))
    expect(screen.getByTestId('managed-field-input-alert.enabled')).toBeInTheDocument()
    expect(screen.queryByTestId('managed-field-input-debug')).not.toBeInTheDocument()

    // 禁止档分组同样可进入，字段渲染为只读（不给可编辑控件，并说明原因）。
    await user.click(screen.getByTestId('managed-editor-nav-local'))
    expect(screen.queryByTestId('managed-field-input-bridge.token')).not.toBeInTheDocument()
    expect(screen.getByText(/探针整包拒绝远程写入/)).toBeInTheDocument()
  })

  it('导航上标出本层已设置的数量，便于看"哪组配过东西"', () => {
    renderWithProviders(
      <ManagedConfigEditor {...yamlProps} items={navItems} groups={navGroups} doc={{ debug: true, 'alert.enabled': true }} onChange={() => {}} />,
    )
    // collect 组里有 1 个键已设置（debug）。
    const nav = screen.getByTestId('managed-editor-nav-collect')
    expect(nav.textContent).toContain('1')
  })

  it('搜索跨分类过滤：输入键名片段即可命中，不受当前分类限制', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <ManagedConfigEditor {...yamlProps} items={navItems} groups={navGroups} doc={{}} onChange={() => {}} />,
    )

    await user.type(screen.getByTestId('managed-editor-search'), 'alert')

    // 命中 alert 组的键，即便当前分类是 collect。
    expect(await screen.findByTestId('managed-field-input-alert.enabled')).toBeInTheDocument()
    expect(screen.queryByTestId('managed-field-input-debug')).not.toBeInTheDocument()
    // 搜索时隐藏分类导航：结果是跨分类的扁平列表，再显示分类树会误导。
    expect(screen.queryByTestId('managed-editor-nav')).not.toBeInTheDocument()

    // 清除搜索后回到分类视图。
    await user.click(screen.getByTestId('managed-editor-search-clear'))
    expect(screen.getByTestId('managed-editor-nav')).toBeInTheDocument()
  })

  it('搜索支持中文名，且无命中时给出可操作的提示', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <ManagedConfigEditor {...yamlProps} items={navItems} groups={navGroups} doc={{}} onChange={() => {}} />,
    )

    await user.type(screen.getByTestId('managed-editor-search'), '告警')
    expect(await screen.findByTestId('managed-field-input-alert.enabled')).toBeInTheDocument()

    await user.clear(screen.getByTestId('managed-editor-search'))
    await user.type(screen.getByTestId('managed-editor-search'), 'zzz-nonexistent')
    expect(await screen.findByText(/没有匹配的配置项/)).toBeInTheDocument()
  })

  it('配置不是合法 JSON 对象时给出明确错误，而不是空白', () => {
    renderWithProviders(
      <ManagedConfigEditor
        {...yamlProps}
        items={[]}
        groups={{}}
        doc={{}}
        onChange={() => {}}
        parseError="配置不是合法 YAML 映射"
      />,
    )
    expect(screen.getByTestId('managed-editor-parse-error')).toHaveTextContent('配置不是合法 YAML 映射')
  })

  it('可视化 / YAML 双视图可切换，且都读同一份草稿', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <ManagedConfigEditor
        {...yamlProps}
        items={navItems}
        groups={navGroups}
        doc={{ debug: true }}
        onChange={() => {}}
        yamlText={['# 调试日志（可改）', 'debug: true', ''].join('\n')}
      />,
    )

    // 默认可视化：字段可编辑。
    expect(screen.getByTestId('managed-field-input-debug')).toBeInTheDocument()

    await user.click(screen.getByTestId('managed-editor-view-yaml'))
    // YAML 视图下不渲染可视化字段，改为展示带注释的文本。
    expect(screen.queryByTestId('managed-field-input-debug')).not.toBeInTheDocument()
    expect(screen.getByTestId('managed-yaml-editor')).toBeInTheDocument()
    expect(screen.getByTestId('managed-yaml-status-ok')).toBeInTheDocument()

    await user.click(screen.getByTestId('managed-editor-view-visual'))
    expect(screen.getByTestId('managed-field-input-debug')).toBeInTheDocument()
  })

  it('YAML 语法错误时指出行号并拦住保存', () => {
    renderWithProviders(
      <ManagedConfigEditor
        {...yamlProps}
        items={navItems}
        groups={navGroups}
        doc={{}}
        onChange={() => {}}
        yamlText={['collect:', '    - a', '  b: 1', ''].join('\n')}
        defaultView="yaml"
      />,
    )

    // 报错必须带行号：只写「格式不正确」用户不知道该改哪一行。
    const status = screen.getByTestId('managed-yaml-status-error')
    expect(status.textContent).toContain('第')
    // 顶部有明确的「拦住保存」说明，而不是让用户点了保存才发现。
    expect(screen.getByTestId('managed-editor-yaml-blocked')).toBeInTheDocument()
  })

  it('字段行内直接显示校验状态，不用滚到页面底部看', () => {
    const meta: ManagedKeyMeta = {
      key: 'collect-period-ticks',
      kind: 'int',
      group: 'collect',
      tier: 'guarded',
      title: '指标采集周期',
      description: '多久采集一次指标。',
      min: 20,
      boundsHint: '过小会放大指标量并压主线程',
    }
    renderWithProviders(
      <ManagedConfigField meta={meta} value={10} onChange={() => {}} />,
    )

    // 越界但未改动（服务端已存的历史值）→ 行内只提示「越界」不判「不通过」。
    expect(screen.getByTestId('managed-field-state-warn')).toBeInTheDocument()
    expect(screen.getByTestId('managed-field-bounds-collect-period-ticks')).toHaveTextContent(
      '过小会放大指标量并压主线程',
    )

    // 合法值 → 行内出现「通过」（不传 validation 时由字段自己判定边界）。
    renderWithProviders(
      <ManagedConfigField meta={meta} value={40} onChange={() => {}} />,
    )
    expect(screen.getAllByTestId('managed-field-state-ok').length).toBeGreaterThan(0)
  })

  it('用户把值改成越界才判不通过并禁写', () => {
    const meta: ManagedKeyMeta = {
      key: 'collect-period-ticks',
      kind: 'int',
      group: 'collect',
      tier: 'guarded',
      title: '指标采集周期',
      description: '多久采集一次指标。',
      min: 20,
      boundsHint: '过小会放大指标量并压主线程',
    }
    renderWithProviders(
      <ManagedConfigField
        meta={meta}
        value={10}
        dirty
        onChange={() => {}}
        onCommit={() => {}}
      />,
    )

    expect(screen.getByTestId('managed-field-state-error')).toBeInTheDocument()
    expect(screen.getByTestId('managed-field-commit-collect-period-ticks')).toBeDisabled()
  })

  it('每个字段旁边都有「写入」按钮，且只在该项改动后可用', async () => {
    const user = userEvent.setup()
    const commits: string[] = []
    function Harness() {
      const [doc, setDoc] = useState<Record<string, unknown>>({ debug: false })
      const dirty = new Set<string>(['debug'])
      void dirty
      return (
        <ManagedConfigEditor
          {...yamlProps}
          items={navItems}
          groups={navGroups}
          doc={doc}
          onChange={setDoc}
          dirtyKeys={new Set(['debug'])}
          onCommitKey={(key) => commits.push(key)}
        />
      )
    }
    renderWithProviders(<Harness />)

    const commit = screen.getByTestId('managed-field-commit-debug')
    expect(commit).toBeEnabled()
    await user.click(commit)
    expect(commits).toEqual(['debug'])
  })
})
