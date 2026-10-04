import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Combobox, type ComboboxOption } from '@jianmanager/ui/components/combobox'
import { useInstanceSearch, type InstanceInfo } from '@/api/instances'
import { useDebounced } from '@/lib/use-debounced'

/** 候选默认窗口：靠键入服务端 q 缩小，与 GroupMembersDialog（FR-336）同款。 */
const CANDIDATE_LIMIT = 50

/** 「全部」哨兵值：Combobox 用空串表示未选，无法承载一个可点选的「全部」项，故另设哨兵。 */
export const INSTANCE_PICKER_ALL = 'all'

interface InstancePickerProps {
  /** 当前值（实例 id）；未选中为 null。与 uuidValue 二选一。 */
  value?: number | null
  /**
   * id 值域下的变更回调。第二参数是选中实例的完整对象（选「全部」或候选里找不到时为 undefined），
   * 供调用方顺手取用 serverPort 之类的字段——否则还得自己再查一次实例列表。
   */
  onChange?: (value: number | null, instance?: InstanceInfo) => void
  /**
   * uuid 值域模式（与 value/onChange 二选一）。下钻选择器（DrillTargetPicker）的 DrillTarget
   * 用 uuid 标识目标，与数字 id 值域互不通用，故单开一组 prop 而不是放宽 value 的类型——
   * 后者会让既有调用方的 onChange 参数退化成联合类型，反而要逐个收窄。
   */
  uuidValue?: string | null
  /** uuid 值域下的变更回调（值为实例 uuid；选「全部」时为 null）。 */
  onUuidChange?: (uuid: string | null, instance?: InstanceInfo) => void
  /**
   * 限定只搜某个节点上的实例。下钻选择器在 node 层只需列出该节点下的实例；
   * 不传则搜全部（服务端 `/instances/search` 原生支持 nodeId 参数）。
   */
  nodeIdFilter?: number
  /** 触发器的可访问名。同一处出现多个选择器时分不清，需要显式区分。 */
  ariaLabel?: string
  /** 当前值的展示名。分页后当前实例可能不在候选窗口内，`isKnownValue` 查不到就会退化成
   *  显示裸 id——父级若已知名字（例如来自列表行、编辑态的原记录）请传进来。 */
  valueLabel?: string
  /** 是否提供「全部」选项（选中即 value=null）。 */
  allowAll?: boolean
  /** 「全部」项的文案；不给则用通用「全部」。各页语境不同（全部实例 / 全部节点）。 */
  allLabel?: string
  /** 只在需要时发请求（例如弹窗打开）。默认 true。 */
  enabled?: boolean
  disabled?: boolean
  /** 校验失败态：触发器加 destructive 边框（透传给 Combobox）。 */
  invalid?: boolean
  id?: string
  className?: string
  /** 触发器占位文案；不给则用通用「请选择」。 */
  placeholder?: string
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
 */
export function InstancePicker({
  value,
  onChange,
  uuidValue,
  onUuidChange,
  nodeIdFilter,
  ariaLabel,
  valueLabel,
  allowAll = false,
  allLabel,
  enabled = true,
  disabled,
  invalid,
  id,
  className,
  placeholder,
}: InstancePickerProps) {
  const { t } = useTranslation()
  const [kw, setKw] = useState('')
  const q = useDebounced(kw, 300).trim()

  const { data: page } = useInstanceSearch(
    {
      ...(q ? { q } : {}),
      ...(nodeIdFilter != null ? { nodeId: nodeIdFilter } : {}),
      page: 1,
      pageSize: CANDIDATE_LIMIT,
      sort: 'name',
      order: 'asc',
    },
    enabled,
  )

  const items = page?.items ?? []
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
  const truncated = page ? page.total > items.length : false

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
        onQueryChange={setKw}
      />
      {truncated && (
        <p className="mt-1 text-xs text-muted-foreground">
          {t('common.searchTruncated', { shown: items.length, total: page?.total ?? 0 })}
        </p>
      )}
    </div>
  )
}
