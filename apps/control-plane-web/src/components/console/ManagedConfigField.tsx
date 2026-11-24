/**
 * @file 探针配置的可视化字段控件。
 *
 * 把「手写 JSON」换成按类型渲染的控件：布尔→开关、数字→带上下限的输入、枚举→下拉、
 * 字符串→文本框、数组→标签列表、alert.rules→规则编辑器。
 *
 * ## 为什么必须由元数据驱动
 * 66 个键各自类型、边界、可选值都不同，硬编码一屏表单既写不完也维护不了。
 * 元数据来自后端 `GET /managed-configs/catalog`（权威源是探针的 config.yml），
 * 这里只负责「按 kind 选控件」，不判断业务规则。
 *
 * ## 为什么禁止档也要显示
 * `bridge.*` / `metrics.*` 等 18 个键禁止远程写入。把它们藏起来会让用户以为
 * 「这个配置项不存在」，而真相是「存在但不能从这里改」——后者才是可解释的。
 * 故按只读行渲染，并注明原因。
 */
import { useState } from 'react'
import { Badge } from '@jianmanager/ui/components/badge'
import { Input } from '@jianmanager/ui/components/input'
import { Label } from '@jianmanager/ui/components/label'
import { Button } from '@jianmanager/ui/components/button'
import { Switch } from '@jianmanager/ui/components/switch'
import { Textarea } from '@jianmanager/ui/components/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { Lock, Plus, Trash2, AlertTriangle, Check, Loader2 } from 'lucide-react'

import type { ManagedKeyMeta } from '@/api/managedConfig'
import { isForbiddenTier } from '@/api/managedConfig'

/** 一条告警规则的值形态（alert.rules 的单个映射项）。 */
export interface AlertRuleValue {
  threshold?: number
  'sustain-cycles'?: number
  level?: string
  enabled?: boolean
}

/** 字段变化的回调：value 为 undefined 表示「本层不设置该键」（回退到低层）。 */
export type FieldChange = (key: string, value: unknown) => void

export function ManagedConfigField({
  meta,
  value,
  onChange,
  validation,
  onCommit,
  dirty,
  committing,
}: {
  meta: ManagedKeyMeta
  value: unknown
  onChange: FieldChange
  /** 本字段的校验态：后端逐键返回的原因 / 本地边界检查。 */
  validation?: FieldValidation
  /**
   * 立即写入这一处改动。
   *
   * 为什么不叫「保存」：这里只写**这一个键**，不是保存整层。语义上和
   * `onChange` 的区别是「已在草稿里」与「已经写进服务端」，用户点一下就知道区别。
   */
  onCommit?: () => void
  /** 本字段有未写入的改动。 */
  dirty?: boolean
  /** 正在写入本字段。 */
  committing?: boolean
}) {
  const forbidden = isForbiddenTier(meta)
  const isSet = value !== undefined
  /**
   * 本字段的最终校验态。
   *
   * 外层传来的 `validation` 是权威结论（后端档位校验 + 页面级判断），
   * 但边界越界这件事字段自己就能判——而且必须自己判：目录里明明写了
   * `min: 20`，界面却等后端（后端根本不管边界）才报错，用户就会看到
   * 一个合法的 10。故这里先做本地判定，外层结论优先覆盖。
   */
  const localError = isOutOfBounds(meta, value) ? meta.boundsHint || describeBounds(meta) : ''
  // 未改动的历史越界值只提示（warn）：值是服务端落盘的旧数据，标红成「不通过」
  // 却又能整层保存，状态自相矛盾；只有用户把它改成越界（dirty）才升 error。
  const state: FieldValidation = localError
    ? { state: dirty ? 'error' : 'warn', message: localError }
    : (validation ?? { state: isSet ? 'ok' : 'idle' })

  /*
    GitHub Settings 式的行式布局：标签与控件同处一行、控件紧跟在标签右侧，
    说明文字在标签下方小字。早先的两栏网格（左标题右控件）在窄屏会折成
    「标题在上、控件在下」两个大块，一屏能看的字段太少；同一行放标签+
    控件+状态后，单字段高度稳定在一行半，且状态就在输入框边上。
  */
  return (
    <div
      className="border-b py-3 last:border-b-0"
      data-testid={`managed-field-${meta.key}`}
      data-field-state={state.state}
    >
      <div className="flex flex-col gap-1.5 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
        <div className="flex min-w-0 flex-col gap-0.5">
          <div className="flex flex-wrap items-center gap-1.5">
            <Label htmlFor={`field-${meta.key}`} className="text-sm font-medium">
              {meta.title}
            </Label>
            {forbidden ? (
              <Badge variant="secondary" className="gap-1 text-[10px]">
                <Lock aria-hidden className="size-3" />
                仅本地
              </Badge>
            ) : meta.tier === 'guarded' ? (
              <Badge variant="outline" className="text-[10px]">
                需谨慎
              </Badge>
            ) : null}
            {isSet ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-5 px-1 text-[10px] text-muted-foreground"
                onClick={() => onChange(meta.key, undefined)}
                data-testid={`managed-field-reset-${meta.key}`}
              >
                重置
              </Button>
            ) : null}
          </div>
          <p className="text-xs leading-snug text-muted-foreground">{meta.description}</p>
          <p className="truncate font-mono text-[10px] text-muted-foreground/60" title={meta.key}>
            {meta.key}
          </p>
        </div>

        {/* 控件 + 状态 + 写入按钮同一行：状态紧挨输入，用户不用离开这一行就知道结果。 */}
        <div className="flex min-w-0 shrink-0 flex-wrap items-center gap-1.5 sm:justify-end">
          {forbidden ? (
            // 只读：展示"为什么不能改"比展示一个禁用控件更有用。
            <p className="max-w-md rounded bg-muted/50 px-2 py-1 text-xs text-muted-foreground">
              开启监听面或承载凭证，探针整包拒绝远程写入。请在实例的 config.yml 里本地修改。
            </p>
          ) : (
            <>
              <FieldControl meta={meta} value={value} onChange={(v) => onChange(meta.key, v)} />
              <FieldState state={state} />
              {onCommit ? (
                <Button
                  type="button"
                  size="xs"
                  variant={dirty ? 'default' : 'outline'}
                  disabled={!dirty || committing || state.state === 'error'}
                  onClick={onCommit}
                  title={dirty ? '把这一项写入服务端' : '没有未写入的改动'}
                  data-testid={`managed-field-commit-${meta.key}`}
                >
                  {committing ? <Loader2 aria-hidden className="animate-spin" /> : null}
                  写入
                </Button>
              ) : null}
            </>
          )}
        </div>
      </div>

      {/* 错误原因贴在本字段下方：后端逐键返回的 message 带键名，这里只留原因部分。 */}
      {(state.state === 'error' || state.state === 'warn') && state.message ? (
        <p
          className={`mt-1 text-xs ${state.state === 'error' ? 'text-destructive' : 'text-amber-600 dark:text-amber-500'}`}
          // 两种来源分开命名，便于用户与测试分清「本地边界」与「后端拒绝」：
          // - 本地越界：`managed-field-bounds-*`（目录里写了 min/max，字段自己就能判）
          // - 后端逐键拒绝：`managed-field-error-*`（禁档 / 未登记档位，只有服务端知道）
          data-testid={
            localError ? `managed-field-bounds-${meta.key}` : `managed-field-error-${meta.key}`
          }
        >
          {state.message}
        </p>
      ) : null}
    </div>
  )
}

/** 单字段校验态。`warn` 与 `error` 时 `message` 必填；warn 只提示不拦截。 */
export interface FieldValidation {
  state: 'idle' | 'ok' | 'warn' | 'error'
  message?: string
}

/** 行内状态标记：通过打勾、失败标红，避免用户来回看页面顶部。 */
function FieldState({ state }: { state: FieldValidation }) {
  if (state.state === 'ok') {
    return (
      <span className="flex items-center gap-1 text-[11px] text-status-success" data-testid="managed-field-state-ok">
        <Check aria-hidden className="size-3.5" />
        通过
      </span>
    )
  }
  if (state.state === 'error') {
    return (
      <span className="flex items-center gap-1 text-[11px] text-destructive" data-testid="managed-field-state-error">
        <AlertTriangle aria-hidden className="size-3.5" />
        不通过
      </span>
    )
  }
  if (state.state === 'warn') {
    return (
      <span className="flex items-center gap-1 text-[11px] text-amber-600 dark:text-amber-500" data-testid="managed-field-state-warn">
        <AlertTriangle aria-hidden className="size-3.5" />
        越界
      </span>
    )
  }
  return null
}

/** 按 kind 分发到具体控件。 */
function FieldControl({
  meta,
  value,
  onChange,
}: {
  meta: ManagedKeyMeta
  value: unknown
  onChange: (value: unknown) => void
}) {
  switch (meta.kind) {
    case 'bool':
      return <BoolControl meta={meta} value={value} onChange={onChange} />
    case 'int':
    case 'float':
      return <NumberControl meta={meta} value={value} onChange={onChange} />
    case 'stringList':
      return <StringListControl meta={meta} value={value} onChange={onChange} />
    case 'object':
      return <ObjectControl meta={meta} value={value} onChange={onChange} />
    case 'string':
    default:
      return <StringControl meta={meta} value={value} onChange={onChange} />
  }
}

/** 布尔：开关。未设置时以默认值显示，但文案标明"当前生效值来自默认"。 */
function BoolControl({
  meta,
  value,
  onChange,
}: {
  meta: ManagedKeyMeta
  value: unknown
  onChange: (value: unknown) => void
}) {
  const fallback = typeof meta.default === 'boolean' ? meta.default : false
  const checked = typeof value === 'boolean' ? value : fallback
  return (
    <div className="flex items-center gap-2">
      <Switch
        id={`field-${meta.key}`}
        checked={checked}
        onCheckedChange={(next) => onChange(next)}
        data-testid={`managed-field-input-${meta.key}`}
      />
      <span className="text-xs text-muted-foreground">
        {checked ? '开启' : '关闭'}
        {value === undefined ? `（本层未设置，按默认 ${fallback ? '开启' : '关闭'} 生效）` : ''}
      </span>
    </div>
  )
}

/** 数字：输入框 + 单位 + 边界说明。不用滑杆——边界可能无界，滑杆表达不了"不限"。 */
function NumberControl({
  meta,
  value,
  onChange,
}: {
  meta: ManagedKeyMeta
  value: unknown
  onChange: (value: unknown) => void
}) {
  const step = meta.kind === 'float' ? 0.1 : 1
  const shown = typeof value === 'number' ? String(value) : value === undefined ? '' : String(value)
  return (
    <div className="flex items-center gap-2">
      <Input
        id={`field-${meta.key}`}
        type="number"
        inputMode="decimal"
        step={step}
        min={meta.min}
        max={meta.max}
        className="h-8 w-40"
        value={shown}
        placeholder={meta.default !== undefined ? `默认 ${String(meta.default)}` : '未设置'}
        onChange={(e) => {
          const raw = e.target.value
          if (raw === '') {
            onChange(undefined)
            return
          }
          const parsed = meta.kind === 'float' ? Number.parseFloat(raw) : Number.parseInt(raw, 10)
          onChange(Number.isNaN(parsed) ? undefined : parsed)
        }}
        data-testid={`managed-field-input-${meta.key}`}
      />
      {meta.unit ? <span className="text-xs text-muted-foreground">{meta.unit}</span> : null}
      <span className="text-xs text-muted-foreground">{describeBounds(meta)}</span>
    </div>
  )
}

/** 字符串：文本框。有 options 时用下拉，避免用户填出探针不认的值。 */
function StringControl({
  meta,
  value,
  onChange,
}: {
  meta: ManagedKeyMeta
  value: unknown
  onChange: (value: unknown) => void
}) {
  const text = value === undefined || value === null ? '' : String(value)
  if (meta.options && meta.options.length > 0) {
    return (
      <Select value={text || undefined} onValueChange={(next) => onChange(next)}>
        <SelectTrigger className="h-8 w-56" data-testid={`managed-field-input-${meta.key}`}>
          <SelectValue placeholder="未设置" />
        </SelectTrigger>
        <SelectContent>
          {meta.options.map((opt) => (
            <SelectItem key={opt} value={opt}>
              {opt}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    )
  }
  return (
    <Input
      id={`field-${meta.key}`}
      className="h-8"
      value={text}
      placeholder={meta.default !== undefined ? `默认 ${String(meta.default)}` : '未设置'}
      onChange={(e) => onChange(e.target.value === '' ? undefined : e.target.value)}
      data-testid={`managed-field-input-${meta.key}`}
    />
  )
}

/** 字符串数组：标签列表（已有项可删，下方输入回车添加）。 */
function StringListControl({
  meta,
  value,
  onChange,
}: {
  meta: ManagedKeyMeta
  value: unknown
  onChange: (value: unknown) => void
}) {
  const items = Array.isArray(value) ? value.map((v) => String(v)) : []
  const [draft, setDraft] = useState('')

  const add = () => {
    const next = draft.trim()
    if (!next) return
    if (items.includes(next)) {
      setDraft('')
      return
    }
    onChange([...items, next])
    setDraft('')
  }

  return (
    <div className="flex flex-col gap-2">
      {items.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {items.map((item, idx) => (
            <Badge key={`${item}-${idx}`} variant="secondary" className="gap-1 font-mono text-xs">
              {item}
              <button
                type="button"
                aria-label={`移除 ${item}`}
                className="text-muted-foreground hover:text-destructive"
                onClick={() => onChange(items.filter((_, i) => i !== idx))}
                data-testid={`managed-field-remove-${meta.key}-${idx}`}
              >
                ×
              </button>
            </Badge>
          ))}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          当前为空{value === undefined ? '（本层未设置，按默认空列表生效）' : ''}
        </p>
      )}
      <div className="flex items-center gap-2">
        <Input
          className="h-8 w-56 font-mono text-xs"
          value={draft}
          placeholder="输入后回车添加"
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              add()
            }
          }}
          data-testid={`managed-field-input-${meta.key}`}
        />
        <Button type="button" variant="outline" size="sm" className="h-8" onClick={add}>
          <Plus aria-hidden className="size-3.5" />
          添加
        </Button>
      </div>
    </div>
  )
}

/**
 * 结构化对象。
 *
 * 目前只服务 `alert.rules`——值是「规则名 → {threshold, sustain-cycles, level, enabled}」。
 * 子字段有元数据时渲染规则编辑器，否则回退 JSON 文本框（不猜结构）。
 */
function ObjectControl({
  meta,
  value,
  onChange,
}: {
  meta: ManagedKeyMeta
  value: unknown
  onChange: (value: unknown) => void
}) {
  if (meta.key === 'alert.rules' && meta.fields && meta.fields.length > 0) {
    return <AlertRulesControl meta={meta} value={value} onChange={onChange} />
  }
  return <JsonFallbackControl meta={meta} value={value} onChange={onChange} />
}

/** 告警规则编辑器：规则名 + 四个字段，增删可视化。 */
function AlertRulesControl({
  meta,
  value,
  onChange,
}: {
  meta: ManagedKeyMeta
  value: unknown
  onChange: (value: unknown) => void
}) {
  const rules = isRecord(value) ? (value as Record<string, AlertRuleValue>) : {}
  const names = Object.keys(rules)
  const [newName, setNewName] = useState('')

  const fieldOf = (name: string) => meta.fields?.find((f) => f.key === name)

  const updateRule = (name: string, patch: Partial<AlertRuleValue>) => {
    onChange({ ...rules, [name]: { ...rules[name], ...patch } })
  }

  const removeRule = (name: string) => {
    const next = { ...rules }
    delete next[name]
    onChange(next)
  }

  const addRule = () => {
    const name = newName.trim()
    if (!name || rules[name]) {
      setNewName('')
      return
    }
    // 新规则用元数据默认值起步，避免用户面对四个空框。
    const seed: AlertRuleValue = {}
    for (const f of meta.fields ?? []) {
      if (f.default !== undefined) {
        ;(seed as Record<string, unknown>)[f.key] = f.default
      }
    }
    onChange({ ...rules, [name]: seed })
    setNewName('')
  }

  return (
    <div className="flex flex-col gap-2" data-testid="managed-alert-rules">
      {names.length === 0 ? (
        <p className="text-xs text-muted-foreground">当前没有规则。添加一条，或保持本层未设置以沿用低层。</p>
      ) : null}

      {names.map((name) => {
        const rule = rules[name] ?? {}
        return (
          <div key={name} className="rounded border bg-muted/20 p-2" data-testid={`managed-alert-rule-${name}`}>
            <div className="mb-1.5 flex items-center gap-2">
              <Badge variant="outline" className="font-mono text-[11px]">
                {name}
              </Badge>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="ml-auto h-6 px-2 text-[11px] text-muted-foreground hover:text-destructive"
                onClick={() => removeRule(name)}
                data-testid={`managed-alert-rule-remove-${name}`}
              >
                <Trash2 aria-hidden className="size-3" />
                删除
              </Button>
            </div>
            <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
              <RuleField
                field={fieldOf('threshold')}
                value={rule.threshold}
                onChange={(v) => updateRule(name, { threshold: v as number | undefined })}
                testId={`managed-alert-rule-${name}-threshold`}
              />
              <RuleField
                field={fieldOf('sustain-cycles')}
                value={rule['sustain-cycles']}
                onChange={(v) => updateRule(name, { 'sustain-cycles': v as number | undefined })}
                testId={`managed-alert-rule-${name}-sustain`}
              />
              <RuleField
                field={fieldOf('level')}
                value={rule.level}
                onChange={(v) => updateRule(name, { level: v as string | undefined })}
                testId={`managed-alert-rule-${name}-level`}
              />
              <RuleField
                field={fieldOf('enabled')}
                value={rule.enabled}
                onChange={(v) => updateRule(name, { enabled: v as boolean | undefined })}
                testId={`managed-alert-rule-${name}-enabled`}
              />
            </div>
          </div>
        )
      })}

      <div className="flex items-center gap-2">
        <Input
          className="h-8 w-52 font-mono text-xs"
          value={newName}
          placeholder="规则名（如 tps-warn）"
          onChange={(e) => setNewName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              addRule()
            }
          }}
          data-testid="managed-alert-rule-new-name"
        />
        <Button type="button" variant="outline" size="sm" className="h-8" onClick={addRule}>
          <Plus aria-hidden className="size-3.5" />
          添加规则
        </Button>
      </div>
    </div>
  )
}

/** 规则里的单个子字段。 */
function RuleField({
  field,
  value,
  onChange,
  testId,
}: {
  field?: ManagedKeyMeta
  value: unknown
  onChange: (value: unknown) => void
  testId: string
}) {
  if (!field) return null
  if (field.kind === 'bool') {
    return (
      <div className="flex items-center gap-1.5">
        <Switch
          checked={typeof value === 'boolean' ? value : Boolean(field.default)}
          onCheckedChange={(next) => onChange(next)}
          data-testid={testId}
        />
        <span className="text-xs">{field.title}</span>
      </div>
    )
  }
  if (field.options && field.options.length > 0) {
    return (
      <label className="flex flex-col gap-1">
        <span className="text-[11px] text-muted-foreground">{field.title}</span>
        <Select value={value === undefined ? undefined : String(value)} onValueChange={onChange}>
          <SelectTrigger className="h-7 w-full" data-testid={testId}>
            <SelectValue placeholder="未设置" />
          </SelectTrigger>
          <SelectContent>
            {field.options.map((opt) => (
              <SelectItem key={opt} value={opt}>
                {opt}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </label>
    )
  }
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[11px] text-muted-foreground">{field.title}</span>
      <Input
        type="number"
        step={field.kind === 'float' ? 0.1 : 1}
        min={field.min}
        max={field.max}
        className="h-7"
        value={value === undefined ? '' : String(value)}
        placeholder={field.default !== undefined ? String(field.default) : '未设置'}
        onChange={(e) => {
          const raw = e.target.value
          if (raw === '') return onChange(undefined)
          const parsed = field.kind === 'float' ? Number.parseFloat(raw) : Number.parseInt(raw, 10)
          onChange(Number.isNaN(parsed) ? undefined : parsed)
        }}
        data-testid={testId}
      />
    </label>
  )
}

/** 无元数据可用的对象：回退 JSON 文本框（不猜结构，猜错会让用户填出探针不认的值）。 */
function JsonFallbackControl({
  meta,
  value,
  onChange,
}: {
  meta: ManagedKeyMeta
  value: unknown
  onChange: (value: unknown) => void
}) {
  const [draft, setDraft] = useState(() =>
    value === undefined ? '' : JSON.stringify(value, null, 2),
  )
  const [error, setError] = useState('')

  return (
    <div className="flex flex-col gap-1">
      <Textarea
        className="min-h-[100px] font-mono text-xs"
        value={draft}
        spellCheck={false}
        placeholder="JSON 对象"
        onChange={(e) => {
          const next = e.target.value
          setDraft(next)
          if (next.trim() === '') {
            setError('')
            onChange(undefined)
            return
          }
          try {
            onChange(JSON.parse(next))
            setError('')
          } catch {
            setError('JSON 格式不正确')
          }
        }}
        data-testid={`managed-field-input-${meta.key}`}
      />
      {error ? <p className="text-xs text-destructive">{error}</p> : null}
    </div>
  )
}

/** 值是否越界（仅数值型）。 */
function isOutOfBounds(meta: ManagedKeyMeta, value: unknown): boolean {
  if (typeof value !== 'number' || Number.isNaN(value)) return false
  if (meta.min !== undefined && value < meta.min) return true
  if (meta.max !== undefined && value > meta.max) return true
  return false
}

/** 边界的人读描述。 */
function describeBounds(meta: ManagedKeyMeta): string {
  if (meta.min !== undefined && meta.max !== undefined) return `${meta.min} ~ ${meta.max}`
  if (meta.min !== undefined) return `≥ ${meta.min}`
  if (meta.max !== undefined) return `≤ ${meta.max}`
  return ''
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}
