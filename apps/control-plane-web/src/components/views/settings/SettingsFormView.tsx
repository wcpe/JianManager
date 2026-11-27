/**
 * @file SettingsFormView：**通用设置表单视图**——把「一组 SettingItem + 一份草稿」渲染成可编辑、即时校验、单按钮保存的表单。
 *       各页面（平台设置、节点设置、采集面板…）都复用它，不再各自手写 `key === 'xxx' ? … : …` 的分支。
 * @input lib/settings/field-types（字段类型声明与分派依据）、lib/settings/settings-draft（草稿模型与提交值组装）、
 *        lib/settings/settings-form（validateSettingDraft，唯一校验入口）、lib/settings/path-overrides（kv-pairs 序列化）、
 *        PathOverridesEditor（成对行编辑器）、Input/Switch/Select/Badge/Button/FieldLabel/FieldError 等原语
 * @output SettingsFormView、SettingsFormViewProps
 * @sync   apps/control-plane-web/src/components/views/settings/SettingsFormView.dom.test.tsx
 * @since  统一设置组件（纯逻辑层之后的视图层）
 */

/**
 * 【这一层的职责边界】
 *  - 它**不含任何业务判断**：字段该长什么样由 `fieldSpecOf(key)` 决定，值合不合法由
 *    `validateSettingDraft(key, value)` 决定，脏不脏由 `hasChanges` 决定，提交什么由
 *    `buildSubmitValues` 决定。视图只负责「按类型分派渲染器 + 把结果画出来」。
 *  - 它**完全受控**：草稿是 props，改动经 `onDraftChange` 上报；怎么落库、保存成功后怎么清草稿
 *    由容器决定（见 settings-draft 的 `clearSubmitted`）。这样同一套表单既能用在平台设置页，
 *    也能用在节点详情里的分段落。
 *  - 校验是**渲染期算**的，不是提交时才跑：用户在第三行输入一个绝对路径，当场就该看到红框。
 */
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { FieldError, FieldLabel } from '@jianmanager/ui/components/field-label'
import { Input } from '@jianmanager/ui/components/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@jianmanager/ui/components/select'
import { Switch } from '@jianmanager/ui/components/switch'
import {
  fieldSpecOf,
  effectTimingOf,
  type EffectTiming,
  usesCustomEditor,
} from '@/lib/settings/field-types'
import {
  buildSubmitValues,
  defaultSerializeKv,
  fromOverrideRows,
  hasChanges,
  shownText,
  toOverrideRows,
  type KvPairRow,
  type SettingItem,
  type SettingsDraft,
} from '@/lib/settings/settings-draft'
import { validateSettingDraft } from '@/lib/settings/settings-form'
import { allRowsValid, parsePathOverrides } from '@/lib/settings/path-overrides'
import { PathOverridesEditor, type InstanceOption } from './PathOverridesEditor'

const EMPTY_SELECT_SENTINEL = '__empty__'

/** 表单字段的控件锚点 id：把点号等字符折成连字符，避免成为无法用选择器命中的 id。 */
function controlIdOf(key: string): string {
  return `setting-input-${key.replace(/[^A-Za-z0-9_-]/g, '-')}`
}

/** 某字段当前**展示的字符串值**（校验与提交比对都用它）。 */
function shownValueOf(item: SettingItem, draft: SettingsDraft): string {
  if (usesCustomEditor(item.key)) {
    const rows = draft.kv[item.key]
    if (rows !== undefined) return defaultSerializeKv(rows)
  }
  return shownText(item, draft)
}

/**
 * 某字段当前的**编辑行**：草稿优先；没动过则回落到已存值解析出来的行。
 *
 * 回落到解析结果而不是空数组，是为了让用户看到「现在配了哪些覆盖」，而不是一张白纸。
 */
function kvRowsOf(item: SettingItem, draft: SettingsDraft): KvPairRow[] {
  const draftRows = draft.kv[item.key]
  if (draftRows !== undefined) return draftRows
  const raw = item.overridden ? item.override : item.platform
  return parsePathOverrides(raw).map((r) => ({ rowKey: r.key, key: r.instanceId, value: r.path }))
}

/** 判断某一项是否存在校验失败（同时覆盖字符串校验与结构化行级校验/重复键检查）。 */
function isItemInvalid(item: SettingItem, draft: SettingsDraft): boolean {
  if (usesCustomEditor(item.key)) {
    const draftRows = draft.kv[item.key]
    if (draftRows !== undefined && !allRowsValid(toOverrideRows(draftRows))) {
      return true
    }
  }
  return validateSettingDraft(item.key, shownValueOf(item, draft)) !== undefined
}

/** 通用设置表单视图的 props。 */
export interface SettingsFormViewProps {
  /** 要渲染的设置项（服务端快照）。 */
  items: readonly SettingItem[]
  /** 当前草稿（受控）。 */
  draft: SettingsDraft
  /** 草稿变更上报（容器负责持有）。 */
  onDraftChange: (next: SettingsDraft) => void
  /** 外部正在保存（与视图内部等待 `onSave` 的态合并）。 */
  saving?: boolean
  /** 保存：由容器决定如何提交，返回是否成功。 */
  onSave: (values: Record<string, string>) => Promise<boolean>
  /** kv-pairs 字段的实例选项（没有就给空数组，编辑器会降级为手填）。 */
  instanceOptions?: readonly InstanceOption[]
  /** 每个字段的展示文案；缺省用 key。 */
  labelOf?: (key: string) => string
  /**
   * 权威的「是否即时生效」，由容器从服务端快照给出（`api/settings.ts` 的
   * `SettingItem.effectiveImmediately` —— 后端逐项给，是唯一权威来源）。
   *
   * 【为什么要这个 prop】`lib/settings/settings-draft.ts` 的 `SettingItem` 没保留
   * 这个字段，而 `FIELD_SPECS` 只有 6 个 key。只靠声明表判定的话，40+ 个可编辑键里
   * 绝大多数会落到「无声明」，界面上一个生效时机都不标——用户反而失去提示。
   *
   * 返回 `undefined`（或未传该 prop）表示这一项没有权威值，此时才回落到声明表。
   */
  effectiveImmediatelyOf?: (key: string) => boolean | undefined
  /**
   * 某个字段旁的**附加动作**（如 `github.token` 的「去 GitHub 创建」）。
   *
   * 【为什么用插槽而不是在组件里判断 key】组件不认识应用侧路由与外部 URL；
   * 一旦在里面写 `if (key === 'github.token')`，通用组件就开始知道业务字段了，
   * 换到别的页面又得改组件。由容器注入具体按钮，组件只负责给它位置。
   */
  actionOf?: (key: string) => ReactNode
}

/** 解析"何时生效"：权威值 > 声明表 > 不标（unknown）。 */
function resolveEffectTiming(
  key: string,
  effectiveImmediatelyOf?: (key: string) => boolean | undefined,
): EffectTiming {
  const authoritative = effectiveImmediatelyOf?.(key)
  if (authoritative !== undefined) return authoritative ? 'immediate' : 'worker'
  return effectTimingOf(key)
}

/** 单个设置项：标签 + 徽标 + 按类型分派的控件 + 行内错误。 */
interface SettingsFieldProps {
  item: SettingItem
  draft: SettingsDraft
  label: string
  instanceOptions: readonly InstanceOption[]
  effectiveImmediatelyOf?: (key: string) => boolean | undefined
  actionOf?: (key: string) => ReactNode
  onTextChange: (key: string, value: string) => void
  onKvChange: (key: string, rows: KvPairRow[]) => void
}

function SettingsField({
  item,
  draft,
  label,
  instanceOptions,
  effectiveImmediatelyOf,
  actionOf,
  onTextChange,
  onKvChange,
}: SettingsFieldProps) {
  const { t } = useTranslation()
  const spec = fieldSpecOf(item.key)
  const value = shownValueOf(item, draft)
  const overrideRows = spec.type === 'kv-pairs' ? toOverrideRows(kvRowsOf(item, draft)) : null
  const kvRowsHasError = overrideRows !== null && !allRowsValid(overrideRows)
  // 唯一的校验入口：不在视图里另写一套规则，避免与后端 / 其它入口口径漂移。
  // kv-pairs 在行编辑器已展示具体行错时，不再叠加面向手写 JSON 的原始报错文案。
  const error = kvRowsHasError ? undefined : validateSettingDraft(item.key, value)
  const controlId = controlIdOf(item.key)
  const testIdKey = item.key.replace(/[^A-Za-z0-9_-]/g, '-')
  const placeholderText = spec.placeholderKey ? t(spec.placeholderKey) : spec.placeholder
  // 非即时生效的字段必须标明「什么时候才生效」，否则用户会以为保存没起作用。
  const workerSide = resolveEffectTiming(item.key, effectiveImmediatelyOf) === 'worker'

  const control = () => {
    switch (spec.type) {
      case 'switch':
        return (
          <div className="flex items-center gap-2">
            <Switch
              id={controlId}
              checked={value === 'true'}
              onCheckedChange={(checked) => onTextChange(item.key, checked ? 'true' : 'false')}
              aria-label={label}
              aria-invalid={error !== undefined ? true : undefined}
            />
            <span className="text-xs text-muted-foreground">
              {value === 'true'
                ? t(spec.onLabel ?? 'common.enabled', '开启')
                : t(spec.offLabel ?? 'common.disabled', '关闭')}
            </span>
          </div>
        )
      case 'select': {
        const selectValue = spec.allowEmpty ? (value === '' ? EMPTY_SELECT_SENTINEL : value) : (value === '' ? undefined : value)
        return (
          <Select
            value={selectValue}
            onValueChange={(next) =>
              onTextChange(item.key, spec.allowEmpty && next === EMPTY_SELECT_SENTINEL ? '' : next)
            }
          >
            <SelectTrigger
              id={controlId}
              size="sm"
              className="w-48"
              aria-label={label}
              aria-invalid={error !== undefined ? true : undefined}
            >
              <SelectValue placeholder={placeholderText ?? t('common.none', '未选择')} />
            </SelectTrigger>
            <SelectContent>
              {spec.allowEmpty && (
                <SelectItem value={EMPTY_SELECT_SENTINEL}>
                  {t('common.default', '默认')}
                </SelectItem>
              )}
              {(spec.options ?? []).map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )
      }
      case 'kv-pairs':
        // 结构化字段交给专门的成对行编辑器：绝不让用户手写 JSON。
        return (
          <PathOverridesEditor
            rows={overrideRows ?? []}
            onChange={(next) => onKvChange(item.key, fromOverrideRows(next))}
            options={instanceOptions}
          />
        )
      default: {
        const isSecret = spec.type === 'secret'
        return (
          <div className="max-w-md">
            <Input
              id={controlId}
              type={isSecret ? 'password' : 'text'}
              value={value}
              onChange={(e) => onTextChange(item.key, e.target.value)}
              inputMode={spec.type === 'numeric' ? 'numeric' : undefined}
              // 凭据类输入关闭自动填充：否则密码管理器会把控制台令牌当成登录口令回填。
              autoComplete={isSecret ? 'new-password' : undefined}
              placeholder={placeholderText}
              aria-label={label}
              aria-invalid={error !== undefined ? true : undefined}
            />
          </div>
        )
      }
    }
  }

  return (
    <div className="border-b px-1 py-3 last:border-b-0" data-testid={`setting-field-${testIdKey}`}>
      <div className="flex flex-wrap items-center gap-2">
        {/* 真正的 label 元素（屏幕阅读器可关联控件）；kv-pairs 的标签对应的是多行编辑器，故不设 htmlFor。 */}
        <FieldLabel htmlFor={spec.type === 'kv-pairs' ? undefined : controlId} className="truncate">
          {label}
        </FieldLabel>
        {item.overridden && (
          <Badge variant="secondary" data-testid={`setting-badge-overridden-${testIdKey}`}>
            {t('settings.overridden', '已覆盖')}
          </Badge>
        )}
        {workerSide && (
          <Badge
            variant="outline"
            title={t(
              'settings.effectTimingHint',
              '此项非在 Control Plane 内即时生效，需改配置/重启或在 Worker 侧生效。',
            )}
            data-testid={`setting-badge-worker-${testIdKey}`}
          >
            {t('settings.workerSide', 'Worker 侧生效')}
          </Badge>
        )}
      </div>
      <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
        <span className="font-mono">{item.key}</span>
        {item.overridden && spec.type !== 'secret' && item.platform !== '' && (
          <span data-testid={`setting-platform-default-${testIdKey}`}>
            {t('nodeLogAcq.platformDefault', '平台默认')}: {item.platform}
          </span>
        )}
      </div>
      <div className="mt-2 flex flex-wrap items-start gap-2">
        {control()}
        {actionOf?.(item.key)}
      </div>
      {/* 校验结果落在行内（而不是只弹 toast）：出错位置就该是错误所在的那一行。 */}
      <FieldError error={error} />
    </div>
  )
}

/** 通用设置表单视图：按字段类型分派渲染，输入即校验，底部单个保存按钮。 */
export function SettingsFormView({
  items,
  draft,
  onDraftChange,
  saving,
  onSave,
  instanceOptions,
  labelOf,
  effectiveImmediatelyOf,
  actionOf,
}: SettingsFormViewProps) {
  const { t } = useTranslation()
  const [awaiting, setAwaiting] = useState(false)
  const busy = awaiting || saving === true

  // 三个派生量都在渲染期算：它们都是纯函数，没有隐藏状态（也就没有 effect 里 setState 的必要）。
  const dirty = hasChanges(items, draft, defaultSerializeKv)
  const invalid = items.some((it) => isItemInvalid(it, draft))
  const canSave = dirty && !invalid && !busy

  const setText = (key: string, value: string) => {
    onDraftChange({ text: { ...draft.text, [key]: value }, kv: draft.kv })
  }
  const setKv = (key: string, rows: KvPairRow[]) => {
    onDraftChange({ text: draft.text, kv: { ...draft.kv, [key]: rows } })
  }

  const save = async () => {
    if (!canSave) return
    setAwaiting(true)
    try {
      // 只交改动的键（buildSubmitValues 已剔除与当前值相同的项）。
      await onSave(buildSubmitValues(items, draft, defaultSerializeKv))
    } finally {
      setAwaiting(false)
    }
  }

  const hint = invalid
    ? t('settings.dirtyInvalid', '存在非法值，修正后才能保存')
    : dirty
      ? t('settings.dirtyHint', '有未保存修改')
      : t('settings.cleanHint', '无改动')

  return (
    <div className="space-y-3" data-testid="settings-form-view">
      {items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('settings.empty', '暂无配置项')}</p>
      ) : (
        items.map((item) => (
          <SettingsField
            key={item.key}
            item={item}
            draft={draft}
            label={labelOf ? labelOf(item.key) : item.key}
            instanceOptions={instanceOptions ?? []}
            effectiveImmediatelyOf={effectiveImmediatelyOf}
            actionOf={actionOf}
            onTextChange={setText}
            onKvChange={setKv}
          />
        ))
      )}

      <div className="flex flex-wrap items-center justify-end gap-3 pt-3">
        <span
          className="text-xs text-muted-foreground"
          data-testid="settings-dirty-hint"
          aria-live="polite"
        >
          {hint}
        </span>
        <Button
          size="sm"
          type="button"
          onClick={() => void save()}
          disabled={!canSave}
          data-testid="settings-save"
        >
          {busy ? t('common.saving', '保存中…') : t('common.save', '保存')}
        </Button>
      </div>
    </div>
  )
}
