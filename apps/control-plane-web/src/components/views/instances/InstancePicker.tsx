import { useTranslation } from 'react-i18next'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'

/** 候选默认窗口：靠键入服务端 q 缩小，与 GroupMembersDialog（FR-336）同款。 */
export const CANDIDATE_LIMIT = 50

/** 「全部」哨兵值：Combobox 用空串表示未选，无法承载一个可点选的「全部」项，故另设哨兵。 */
export const INSTANCE_PICKER_ALL = 'all'

/** 候选实例的最小结构：视图只用这三个字段做匹配与展示，其余字段原样透传回调用方。 */
export interface InstancePickerItemView {
  id: number
  uuid: string
  name: string
}

/**
 * 服务端搜索的实例选择器（面向千级 / 万级实例）。
 *
 * 【为什么需要它】原先各处用 `useInstances()` 拉全量再 map 成下拉选项：1200 实例约 1MB，
 * 万级就是十几 MB，而且它带 30 秒兜底轮询，每轮都重传整份。本组件改为「默认取前 N 条 +
 * 键入走服务端 q」，与 GroupMembersDialog（FR-336）同一模式。
 *
 * 【为什么不用 Radix Select】Select 依赖全部 SelectItem mount 才能提供首字母跳转与方向键
 * 导航，因此无法只渲染前 N 项；千级候选项下它每次 render 还要把整个选项列表求值一遍。
 * Combobox 基于 Popover（关闭不挂 DOM）+ 有界渲染 + 服务端搜索，才是大规模下的正解。
 *
 * 受控视图（ADR-097 a 范式）：**不取数**——候选窗口与总数经 props 注入，键入经
 * `onQueryChange` 上报（防抖与请求都由外壳负责：那是「何时发请求」的策略）。
 *
 * 泛型说明：`items` 的元素类型由外壳决定并原样回传（`T`），调用方因此仍能拿到完整实例
 * 对象（例如顺手取 `serverPort`），而不必再查一次列表。视图自身只消费 `id`/`uuid`/`name`。
 */
export interface InstancePickerProps<T extends InstancePickerItemView = InstancePickerItemView> {
  /** 当前值（实例 id）；未选中为 null。与 uuidValue 二选一。 */
  value?: number | null
  /**
   * id 值域下的变更回调。第二参数是选中实例的完整对象（选「全部」或候选里找不到时为 undefined），
   * 供调用方顺手取用 serverPort 之类的字段——否则还得自己再查一次实例列表。
   */
  onChange?: (value: number | null, instance?: T) => void
  /**
   * uuid 值域模式（与 value/onChange 二选一）。下钻选择器（DrillTargetPicker）的 DrillTarget
   * 用 uuid 标识目标，与数字 id 值域互不通用，故单开一组 prop 而不是放宽 value 的类型——
   * 后者会让既有调用方的 onChange 参数退化成联合类型，反而要逐个收窄。
   */
  uuidValue?: string | null
  /** uuid 值域下的变更回调（值为实例 uuid；选「全部」时为 null）。 */
  onUuidChange?: (uuid: string | null, instance?: T) => void
  /** 候选窗口（外壳取数注入；服务端已按 nodeId 过滤、按名排序、截断到 CANDIDATE_LIMIT）。 */
  items?: T[]
  /** 候选总数（服务端返回），用于截断提示；不给则不提示。 */
  total?: number
  /** 键入关键字上报（外壳做防抖后下发服务端 q）。 */
  onQueryChange?: (keyword: string) => void
  /** 触发器的可访问名。同一处出现多个选择器时分不清，需要显式区分。 */
  ariaLabel?: string
  /** 当前值的展示名。分页后当前实例可能不在候选窗口内，`isKnownValue` 查不到就会退化成
   *  显示裸 id——父级若已知名字（例如来自列表行、编辑态的原记录）请传进来。 */
  valueLabel?: string
  /** 是否提供「全部」选项（选中即 value=null）。 */
  allowAll?: boolean
  /** 「全部」项的文案；不给则用通用「全部」。各页语境不同（全部实例 / 全部节点）。 */
  allLabel?: string
  disabled?: boolean
  /** 校验失败态：触发器加 destructive 边框（透传给 Combobox）。 */
  invalid?: boolean
  id?: string
  className?: string
  /** 触发器占位文案；不给则用通用「请选择」。 */
  placeholder?: string
}

export function InstancePicker<T extends InstancePickerItemView = InstancePickerItemView>({
  value,
  onChange,
  uuidValue,
  onUuidChange,
  items = [],
  total,
  onQueryChange,
  ariaLabel,
  valueLabel,
  allowAll = false,
  allLabel,
  disabled,
  invalid,
  id,
  className,
  placeholder,
}: InstancePickerProps<T>) {
  const { t } = useTranslation()

  // uuid 值域模式：只要给了 uuidValue 或 onUuidChange 就走它（下钻选择器的目标以 uuid 标识）。
  const uuidMode = uuidValue !== undefined || onUuidChange !== undefined
  const currentRaw = uuidMode ? uuidValue : value == null ? null : String(value)
  // 未选中时：允许「全部」用哨兵值（Combobox 无法用一个可点选的空串项表达「全部」），
  // 不允许时用空串——空串才会让 Combobox 显示 placeholder，否则会显示裸的哨兵值。
  const current = currentRaw == null ? (allowAll ? INSTANCE_PICKER_ALL : '') : currentRaw

  const options: ComboboxOption[] = [
    ...(allowAll ? [{ value: INSTANCE_PICKER_ALL, label: allLabel ?? t('common.all') }] : []),
    ...items.map((i) => ({ value: uuidMode ? i.uuid : String(i.id), label: i.name })),
    // 回显兜底：当前值不在候选窗口内时补一个选项，避免触发器显示裸 id / uuid 或留空。
    ...(currentRaw != null &&
    currentRaw !== '' &&
    valueLabel &&
    !items.some((i) => (uuidMode ? i.uuid : String(i.id)) === currentRaw)
      ? [{ value: currentRaw, label: valueLabel }]
      : []),
  ]

  // 服务端截断时提示引导键入（与 GroupMembersDialog 一致）。
  const truncated = total !== undefined && total > items.length

  return (
    <div className={className}>
      <Combobox
        id={id}
        ariaLabel={ariaLabel}
        options={options}
        value={current}
        onChange={(v) => {
          if (v === INSTANCE_PICKER_ALL) {
            if (uuidMode) onUuidChange?.(null)
            else onChange?.(null)
            return
          }
          const inst = items.find((i) => (uuidMode ? i.uuid === v : String(i.id) === v))
          if (uuidMode) onUuidChange?.(v, inst)
          else onChange?.(Number(v), inst)
        }}
        placeholder={placeholder ?? t('common.selectPlaceholder')}
        allowCustom={false}
        disabled={disabled}
        invalid={invalid}
        onQueryChange={onQueryChange}
      />
      {truncated && (
        <p className="mt-1 text-xs text-muted-foreground">
          {t('common.searchTruncated', { shown: items.length, total: total ?? 0 })}
        </p>
      )}
    </div>
  )
}
