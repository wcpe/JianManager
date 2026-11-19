/**
 * SettingsFormView 的 DOM 强断言。
 *
 * 这里测的是**视图分派与交互**（哪个字段派生出哪种控件、输入后是否当场报错、保存按钮的使能与文案、
 * 提交内容是否只含改动项）。字段类型的声明规律、草稿的脏查询算法、校验规则本身都在
 * `lib/settings/*.test.ts` 里单测，不在这里重复——否则同一份规则会有两个事实源。
 */
import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import '@/i18n'
import { SettingsFormView } from './SettingsFormView'
import { emptyDraft, type SettingItem, type SettingsDraft } from '@/lib/settings/settings-draft'

/**
 * Radix Select 展开/选项滚动时会调 `hasPointerCapture` / `scrollIntoView`，jsdom 两个都没实现。
 * 不垫片的话失败信息是 `TypeError: ... is not a function`，与“控件到底渲染没渲染”无关。
 */
HTMLElement.prototype.hasPointerCapture ??= () => false
HTMLElement.prototype.scrollIntoView ??= () => {}

/** 造一条服务端快照：默认未覆盖、override 为空。 */
function item(key: string, platform: string, overrides: Partial<SettingItem> = {}): SettingItem {
  return { key, platform, override: '', overridden: false, ...overrides }
}

/** 每个用例都至少带上这四种控件类型，以免“只有 text 字段时全绿”这种假绿。 */
const items: SettingItem[] = [
  item('graceful_stop.timeout', '30s'),
  item('debug.mode', 'false'),
  item('log.level', 'info'),
  item('github.token', ''),
  item('backup.retention_days', '7'),
  item('log.acq.path_overrides', ''),
]

interface HarnessProps {
  items?: readonly SettingItem[]
  initialDraft?: SettingsDraft
  saving?: boolean
  onSave?: (values: Record<string, string>) => Promise<boolean>
  onDraftChange?: (next: SettingsDraft) => void
}

/**
 * 受控宿主：组件本身不持草稿，因此测试必须自己把 onChange 接回去，否则“输入”只是上报了却没重渲染，
 * 断言会测到一个永远不更新的界面。
 */
function Harness({
  items: list = items,
  initialDraft,
  saving,
  onSave = vi.fn().mockResolvedValue(true),
  onDraftChange,
}: HarnessProps) {
  const [draft, setDraft] = useState<SettingsDraft>(initialDraft ?? emptyDraft())
  return (
    <SettingsFormView
      items={list}
      draft={draft}
      onDraftChange={(next) => {
        onDraftChange?.(next)
        setDraft(next)
      }}
      saving={saving}
      onSave={onSave}
      labelOf={(key) => `标签:${key}`}
    />
  )
}

/** 取某个字段所在的行（按 key 折成 testid 后缀）。 */
function fieldOf(key: string): HTMLElement {
  return screen.getByTestId(`setting-field-${key.replace(/[^A-Za-z0-9_-]/g, '-')}`)
}

function saveButton(): HTMLElement {
  return screen.getByTestId('settings-save')
}

describe('SettingsFormView 按字段类型分派渲染器', () => {
  it('text→input[type=text]、secret→input[type=password]、numeric→inputmode=numeric', () => {
    render(<Harness />)

    const text = within(fieldOf('graceful_stop.timeout')).getByRole('textbox')
    expect(text).toHaveAttribute('type', 'text')
    expect(text).toHaveValue('30s')

    // secret：密码框 + 关自动填充（否则密码管理器会把令牌当登录口令回填）。
    const secret = fieldOf('github.token').querySelector('input')
    expect(secret).toHaveAttribute('type', 'password')
    expect(secret).toHaveAttribute('autocomplete', 'new-password')

    // numeric：仍是文本输入，但弹数字键盘。
    const numeric = within(fieldOf('backup.retention_days')).getByRole('textbox')
    expect(numeric).toHaveAttribute('inputmode', 'numeric')
    expect(numeric).toHaveValue('7')
  })

  it('switch→role=switch，切换后上报 "true"', async () => {
    const onDraftChange = vi.fn()
    render(<Harness onDraftChange={onDraftChange} />)

    const sw = within(fieldOf('debug.mode')).getByRole('switch')
    expect(sw).not.toBeChecked()

    await userEvent.click(sw)
    await waitFor(() => expect(within(fieldOf('debug.mode')).getByRole('switch')).toBeChecked())
    const last = onDraftChange.mock.calls.at(-1)?.[0] as SettingsDraft
    // 提交值仍是字符串 "true"/"false"，后端契约不变。
    expect(last.text['debug.mode']).toBe('true')
  })

  it('select→combobox，当前值可见', () => {
    render(<Harness />)
    const trigger = within(fieldOf('log.level')).getByRole('combobox')
    expect(trigger).toHaveTextContent('info')
  })

  it('kv-pairs→出现成对行编辑器（而不是 JSON 输入框）', () => {
    render(<Harness />)
    const field = fieldOf('log.acq.path_overrides')
    expect(within(field).getByTestId('path-overrides-editor')).toBeInTheDocument()
    // 这个字段不能被当成普通文本框：否则等于退回“手写 JSON”。
    expect(within(field).queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('kv-pairs 回显已存的覆盖（否则用户会看到空白而以为没配）', () => {
    render(
      <Harness
        items={[item('log.acq.path_overrides', '', { override: '{"inst:3":"logs/server.log"}', overridden: true })]}
      />,
    )
    expect(screen.getByTestId('path-override-path-kvrow-0')).toHaveValue('logs/server.log')
    expect(screen.getByTestId('path-override-instance-kvrow-0')).toHaveValue('3')
  })
})

describe('SettingsFormView 输入即校验', () => {
  it('非数字的保留天数当场报错（aria-invalid + 行内文案）', async () => {
    render(<Harness />)
    const input = within(fieldOf('backup.retention_days')).getByRole('textbox')
    expect(input).not.toHaveAttribute('aria-invalid')
    expect(screen.queryByText('须为非负整数')).not.toBeInTheDocument()

    await userEvent.clear(input)
    await userEvent.type(input, '7d')

    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(within(fieldOf('backup.retention_days')).getByText('须为非负整数')).toBeInTheDocument()
    // 非法值存在时保存按钮必须关上（不是提交后才 422）。
    expect(saveButton()).toBeDisabled()
  })

  it('非法 duration 当场报错', async () => {
    render(<Harness />)
    const input = within(fieldOf('graceful_stop.timeout')).getByRole('textbox')
    await userEvent.clear(input)
    await userEvent.type(input, '30')

    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(within(fieldOf('graceful_stop.timeout')).getByText(/须为正的时长/)).toBeInTheDocument()
  })
})

describe('SettingsFormView 保存按钮态', () => {
  it('无改动时禁用，并提示「无改动」', () => {
    render(<Harness />)
    expect(saveButton()).toBeDisabled()
    expect(saveButton()).toHaveTextContent('保存')
    expect(screen.getByTestId('settings-dirty-hint')).toHaveTextContent('无改动')
  })

  it('有改动时可点，并提示「有未保存修改」', async () => {
    render(<Harness />)
    const input = within(fieldOf('graceful_stop.timeout')).getByRole('textbox')
    await userEvent.clear(input)
    await userEvent.type(input, '45s')

    await waitFor(() => expect(saveButton()).toBeEnabled())
    expect(screen.getByTestId('settings-dirty-hint')).toHaveTextContent('有未保存修改')
  })

  it('改回原值后重新禁用（脏判定不能只看“动过没有”）', async () => {
    render(<Harness />)
    const input = within(fieldOf('log.level')).parentElement?.querySelector('[role="combobox"]')
    expect(input).not.toBeNull()

    const text = within(fieldOf('graceful_stop.timeout')).getByRole('textbox')
    await userEvent.clear(text)
    await userEvent.type(text, '45s')
    await waitFor(() => expect(saveButton()).toBeEnabled())

    await userEvent.clear(text)
    await userEvent.type(text, '30s')
    await waitFor(() => expect(saveButton()).toBeDisabled())
  })

  it('保存中按钮禁用且文案变化', async () => {
    let release: (ok: boolean) => void = () => {}
    const onSave = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          release = resolve
        }),
    )
    render(<Harness onSave={onSave} />)

    const input = within(fieldOf('graceful_stop.timeout')).getByRole('textbox')
    await userEvent.clear(input)
    await userEvent.type(input, '45s')
    await waitFor(() => expect(saveButton()).toBeEnabled())

    await userEvent.click(saveButton())
    await waitFor(() => expect(saveButton()).toHaveTextContent(/保存中/))
    expect(saveButton()).toBeDisabled()

    release(true)
    await waitFor(() => expect(saveButton()).toHaveTextContent('保存'))
  })

  it('外部 saving 也禁用（容器在别处保存时不重复提交）', () => {
    render(<Harness saving />)
    expect(saveButton()).toBeDisabled()
    expect(saveButton()).toHaveTextContent(/保存中/)
  })
})

describe('SettingsFormView 提交内容与时效徽标', () => {
  it('提交的 values 只含改动的键', async () => {
    const onSave = vi.fn().mockResolvedValue(true)
    render(<Harness onSave={onSave} />)

    const input = within(fieldOf('backup.retention_days')).getByRole('textbox')
    await userEvent.clear(input)
    await userEvent.type(input, '14')
    await waitFor(() => expect(saveButton()).toBeEnabled())

    await userEvent.click(saveButton())
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    // 未动过的 graceful_stop.timeout / log.level 等一律不进提交体。
    expect(onSave.mock.calls[0]?.[0]).toEqual({ 'backup.retention_days': '14' })
  })

  it('kv-pairs 的改动序列化成后端契约的 JSON 文本', async () => {
    const onSave = vi.fn().mockResolvedValue(true)
    render(<Harness onSave={onSave} />)

    await userEvent.click(screen.getByTestId('path-overrides-add'))
    await userEvent.type(screen.getByTestId('path-override-instance-kvrow-0'), '1')
    await userEvent.type(screen.getByTestId('path-override-path-kvrow-0'), 'logs/a.log')

    await waitFor(() => expect(saveButton()).toBeEnabled())
    await userEvent.click(saveButton())

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(onSave.mock.calls[0]?.[0]).toEqual({
      'log.acq.path_overrides': '{"inst:1":"logs/a.log"}',
    })
  })

  it('「Worker 侧生效」徽标只出现在非即时字段上', () => {
    render(<Harness />)
    // 非即时：保留天数、路径覆盖。
    expect(screen.getByTestId('setting-badge-worker-backup-retention_days')).toHaveTextContent(
      'Worker 侧生效',
    )
    expect(screen.getByTestId('setting-badge-worker-log-acq-path_overrides')).toHaveTextContent(
      'Worker 侧生效',
    )
    // 即时：调试开关、日志级别、令牌。
    expect(screen.queryByTestId('setting-badge-worker-debug-mode')).not.toBeInTheDocument()
    expect(screen.queryByTestId('setting-badge-worker-log-level')).not.toBeInTheDocument()
    expect(screen.queryByTestId('setting-badge-worker-github-token')).not.toBeInTheDocument()
  })

  it('「已覆盖」徽标只出现在 overridden 的字段上', () => {
    render(
      <Harness
        items={[items[0]!, item('log.level', 'info', { override: 'debug', overridden: true })]}
      />,
    )
    expect(screen.getByTestId('setting-badge-overridden-log-level')).toHaveTextContent('已覆盖')
    expect(
      screen.queryByTestId('setting-badge-overridden-graceful_stop-timeout'),
    ).not.toBeInTheDocument()
  })

  it('空清单给说明而不是一片空白', () => {
    render(<Harness items={[]} />)
    expect(screen.getByTestId('settings-form-view')).toHaveTextContent('暂无配置项')
  })
})

/**
 * 生效时机徽标：权威值 > 声明表 > 不标。
 *
 * 【上一版错在哪】把「未登记 spec」默认当成「非即时生效」，于是给
 * `traffic.rate_limit.*` 这类其实即时生效的配置打了「Worker 侧生效」——用户会去
 * 重启 Worker 找一个根本不存在的开关。**标错比不标更糟**。
 *
 * 【权威值从哪来】后端 `GET /settings` 的每一项都带 `effectiveImmediately`，
 * 只是 `lib/settings/settings-draft.ts` 的 SettingItem 没保留它。所以这里用
 * `effectiveImmediatelyOf` 把这个值透给视图，而不是靠只有 6 个 key 的声明表去猜
 * 40+ 个可编辑键的生效时机。
 */
describe('SettingsFormView 生效时机徽标', () => {
  it('容器给了权威值就以它为准：后端说即时生效 → 不标（即使声明表没有这一项）', () => {
    render(
      <SettingsFormView
        items={[item('traffic.rate_limit.qps', '100')]}
        draft={emptyDraft()}
        onDraftChange={vi.fn()}
        onSave={vi.fn().mockResolvedValue(true)}
        effectiveImmediatelyOf={() => true}
      />,
    )
    expect(
      screen.queryByTestId('setting-badge-worker-traffic-rate_limit-qps'),
    ).not.toBeInTheDocument()
  })

  it('权威值说非即时生效 → 标「Worker 侧生效」（声明表没登记也要标）', () => {
    render(
      <SettingsFormView
        items={[item('log.acq.gz_scan_interval', '10m')]}
        draft={emptyDraft()}
        onDraftChange={vi.fn()}
        onSave={vi.fn().mockResolvedValue(true)}
        effectiveImmediatelyOf={() => false}
      />,
    )
    expect(
      screen.getByTestId('setting-badge-worker-log-acq-gz_scan_interval'),
    ).toHaveTextContent('Worker 侧生效')
  })

  it('权威值优先于声明表：声明表说非即时，后端说即时 → 不标', () => {
    // backup.retention_days 在 FIELD_SPECS 里是 effectiveImmediately: false
    render(
      <SettingsFormView
        items={[item('backup.retention_days', '7')]}
        draft={emptyDraft()}
        onDraftChange={vi.fn()}
        onSave={vi.fn().mockResolvedValue(true)}
        effectiveImmediatelyOf={() => true}
      />,
    )
    expect(
      screen.queryByTestId('setting-badge-worker-backup-retention_days'),
    ).not.toBeInTheDocument()
  })

  it('没有权威值时回落声明表：已声明非即时的仍标', () => {
    render(
      <SettingsFormView
        items={[item('backup.retention_days', '7')]}
        draft={emptyDraft()}
        onDraftChange={vi.fn()}
        onSave={vi.fn().mockResolvedValue(true)}
      />,
    )
    expect(
      screen.getByTestId('setting-badge-worker-backup-retention_days'),
    ).toBeInTheDocument()
  })

  it('既无权威值又未声明 spec → 不标（未知时不猜，标错比不标更糟）', () => {
    render(
      <SettingsFormView
        items={[item('traffic.rate_limit.qps', '100')]}
        draft={emptyDraft()}
        onDraftChange={vi.fn()}
        onSave={vi.fn().mockResolvedValue(true)}
      />,
    )
    expect(
      screen.queryByTestId('setting-badge-worker-traffic-rate_limit-qps'),
    ).not.toBeInTheDocument()
  })

  it('权威值返回 undefined 时等价于"没有权威值"，回落声明表', () => {
    render(
      <SettingsFormView
        items={[item('backup.retention_days', '7')]}
        draft={emptyDraft()}
        onDraftChange={vi.fn()}
        onSave={vi.fn().mockResolvedValue(true)}
        effectiveImmediatelyOf={() => undefined}
      />,
    )
    expect(
      screen.getByTestId('setting-badge-worker-backup-retention_days'),
    ).toBeInTheDocument()
  })
})
