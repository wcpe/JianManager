/**
 * @file 探针托管配置的可视化编辑器。
 *
 * 取代原来的「240px 高 JSON 文本框」：按 `GET /managed-configs/catalog` 的元数据
 * 分组渲染控件，用户不需要记 66 个点分键和它们的取值范围。
 *
 * ## 版式：GitHub Settings 式（左侧固定导航 + 右侧单列行式表单）
 * 参考 GitHub 仓库设置页：左栏是一列可点的分类，右栏每个设置项占一行
 * （标题+说明在左、控件在右），操作按钮紧贴各自的控件而不是压到页面底部。
 * 之前把「校验/预演/保存」堆在页面最下方，用户改完一个数字要滚到底才知道
 * 有没有问题——现在每个字段自带状态与「写入」按钮，全部走完后底部才出现
 * 一次性的「保存整层」。
 *
 * ## 三层值语义（界面必须讲清，否则用户会误以为保存就是生效）
 * 每层只保存**自己写过的键**（增量）。本层没设置的键 `undefined`，合并时回退到低层。
 * 因此界面对每个字段区分三种状态：
 *   1. 本层已设置 → 显示值 + 「重置为本层未设置」；
 *   2. 本层未设置但低层有值 → 显示低层生效值（只读提示）；
 *   3. 谁都没设置 → 显示默认值提示。
 * 混淆这三者会导致「我明明删了它却还在生效」这类难以解释的现象。
 *
 * ## 高级 YAML 视图
 * 可视化控件覆盖不到的部分（未来新增键、复杂嵌套）保留 YAML 入口，
 * 注释由目录元数据自动生成。两个视图编辑同一份草稿，切换不丢内容。
 */
import { useMemo, useState } from 'react'
import { Alert, AlertDescription, AlertTitle } from '@jianmanager/ui/components/alert'
import { Badge } from '@jianmanager/ui/components/badge'
import { Button } from '@jianmanager/ui/components/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@jianmanager/ui/components/collapsible'
import { Input } from '@jianmanager/ui/components/input'
import { AlertTriangle, ChevronDown, Info, Search, X } from 'lucide-react'

import type { ManagedKeyGroup, ManagedKeyMeta } from '@/api/managedConfig'
import { flattenManagedConfig, unflattenManagedConfig } from '@/api/managedConfig'
import { ManagedConfigField, type FieldValidation } from '@/components/console/ManagedConfigField'
import { ManagedYamlEditor } from '@/components/console/ManagedYamlEditor'

/** 分组渲染顺序（与探针 config.yml 的段落顺序一致，运维按熟悉顺序找键）。 */
const GROUP_ORDER: ManagedKeyGroup[] = [
  'collect',
  'world',
  'history',
  'alert',
  'cpu',
  'http',
  'forensic',
  'mcp',
  'managed',
  'folia',
  'incision',
  'local',
]

export interface ManagedConfigEditorProps {
  /** 目录元数据（后端权威）。 */
  items: ManagedKeyMeta[]
  /** 分组中文名（后端维护）。 */
  groups: Record<string, string>
  /** 本层的嵌套配置对象（草稿）。 */
  doc: Record<string, unknown>
  /** 草稿变化（仍是嵌套对象，由外层负责序列化提交）。 */
  onChange: (doc: Record<string, unknown>) => void
  /** 低层合并后的生效值（点分键 → 值），用于提示"未设置时实际生效什么"。 */
  inherited?: Record<string, unknown>
  /** JSON 解析错误（外层解析草稿失败时传入）。 */
  parseError?: string
  /**
   * 逐字段校验结果（点分键 → 状态）。
   *
   * 来源有两条：后端 `/managed-configs/validate` 的逐键拒绝原因（禁档 / 未登记），
   * 以及本地边界检查。两者都归到字段行上，用户不必去页面底部找错误。
   */
  fieldValidations?: Record<string, FieldValidation>
  /** 相对上次保存有未写入改动的键（用于「写入」按钮高亮）。 */
  dirtyKeys?: Set<string>
  /** 写入单个键（只落盘该键，不是保存整层）。 */
  onCommitKey?: (key: string) => void
  /** 正在写入的键。 */
  committingKey?: string | null
  /** YAML 视图与可视化视图的切换（默认可视化）。 */
  defaultView?: ManagedEditorView
  /** 当前 YAML 文本（唯一真值，由页面持有）。 */
  yamlText: string
  /** YAML 文本变化（页面写回草稿）。 */
  onYamlChange: (text: string) => void
  /** 用当前解析结果重建一份带注释的 YAML。 */
  onRegenerateYaml: () => void
}

/** 编辑器视图：可视化表单 / YAML 编辑。 */
export type ManagedEditorView = 'visual' | 'yaml'

/** 一个分组及其下的键。 */
interface GroupBucket {
  group: ManagedKeyGroup
  label: string
  items: ManagedKeyMeta[]
}

/**
 * 配置编辑器：分类导航 + 当前分类表单 + 高级 YAML 折叠区。
 */
export function ManagedConfigEditor({
  items,
  groups,
  doc,
  onChange,
  inherited,
  parseError,
  fieldValidations,
  dirtyKeys,
  onCommitKey,
  committingKey,
  defaultView = 'visual',
  yamlText,
  onYamlChange,
  onRegenerateYaml,
}: ManagedConfigEditorProps) {
  // 扁平视图：编辑器按点分键工作（与后端合并口径一致）。
  const flat = useMemo(() => flattenManagedConfig(doc), [doc])
  const [view, setView] = useState<ManagedEditorView>(defaultView)
  const [yamlOpen, setYamlOpen] = useState(false)
  const [yamlValid, setYamlValid] = useState(true)
  const [keyword, setKeyword] = useState('')

  // 按分组聚合（顺序固定；目录里出现 GROUP_ORDER 未覆盖的分组时追加在后面，不让键消失）。
  const buckets = useMemo<GroupBucket[]>(() => {
    const map = new Map<ManagedKeyGroup, ManagedKeyMeta[]>()
    for (const meta of items) {
      const list = map.get(meta.group) ?? []
      list.push(meta)
      map.set(meta.group, list)
    }
    const ordered: GroupBucket[] = []
    for (const group of GROUP_ORDER) {
      const list = map.get(group)
      if (list && list.length > 0) {
        ordered.push({ group, label: groups[group] ?? group, items: list })
        map.delete(group)
      }
    }
    for (const [group, list] of map) {
      ordered.push({ group, label: groups[group] ?? group, items: list })
    }
    return ordered
  }, [items, groups])

  const [activeGroup, setActiveGroup] = useState<ManagedKeyGroup | null>(null)
  // 默认停在第一个分组——否则右侧空白，用户以为没加载出来。
  const currentGroup = activeGroup ?? buckets[0]?.group ?? null

  // 搜索：键名（含点分路径）或中文标题/说明命中即算。跨分类过滤。
  const query = keyword.trim().toLowerCase()
  const searchHits = useMemo(() => {
    if (!query) return null
    return items.filter(
      (meta) =>
        meta.key.toLowerCase().includes(query) ||
        meta.title.toLowerCase().includes(query) ||
        meta.description.toLowerCase().includes(query),
    )
  }, [items, query])

  const setKey = (key: string, value: unknown) => {
    const next = { ...flat }
    if (value === undefined) {
      delete next[key]
    } else {
      next[key] = value
    }
    onChange(unflattenManagedConfig(next))
  }

  /** 某分组里有几个键已被本层设置（导航上标出来，便于看"哪组配过东西"）。 */
  const setCountOf = (bucket: GroupBucket) =>
    bucket.items.reduce((n, meta) => n + (flat[meta.key] !== undefined ? 1 : 0), 0)

  /** 某分组里有几个键校验失败（导航上标红点，防止用户在别的分组里漏看）。 */
  const errorCountOf = (bucket: GroupBucket) =>
    bucket.items.reduce((n, meta) => n + (fieldValidations?.[meta.key]?.state === 'error' ? 1 : 0), 0)

  const setCount = Object.keys(flat).length
  const totalErrors = useMemo(
    () => Object.values(fieldValidations ?? {}).filter((v) => v.state === 'error').length,
    [fieldValidations],
  )
  const currentBucket = buckets.find((b) => b.group === currentGroup)
  const visibleItems: ManagedKeyMeta[] = searchHits ?? currentBucket?.items ?? []

  return (
    <div className="flex flex-col gap-3" data-testid="managed-layer-config">
      {parseError ? (
        <Alert variant="destructive" data-testid="managed-editor-parse-error">
          <AlertTriangle aria-hidden />
          <AlertTitle>配置内容无法解析</AlertTitle>
          <AlertDescription className="text-xs">{parseError}</AlertDescription>
        </Alert>
      ) : null}

      {/* 顶栏：视图切换 + 搜索 + 全局状态。一条横线，不做成大卡片。 */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="inline-flex rounded-md border p-0.5" role="tablist" aria-label="编辑视图">
          <button
            type="button"
            role="tab"
            aria-selected={view === 'visual'}
            onClick={() => setView('visual')}
            className={`rounded px-2.5 py-1 text-xs transition-colors ${
              view === 'visual' ? 'bg-primary/10 font-medium text-primary' : 'text-muted-foreground hover:bg-muted'
            }`}
            data-testid="managed-editor-view-visual"
          >
            可视化
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={view === 'yaml'}
            onClick={() => setView('yaml')}
            className={`rounded px-2.5 py-1 text-xs transition-colors ${
              view === 'yaml' ? 'bg-primary/10 font-medium text-primary' : 'text-muted-foreground hover:bg-muted'
            }`}
            data-testid="managed-editor-view-yaml"
          >
            YAML
          </button>
        </div>

        <div className="relative min-w-[180px] flex-1">
          <Search
            aria-hidden
            className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            className="h-8 pl-8 pr-8 text-xs"
            value={keyword}
            placeholder="搜索配置项：键名（collect-period-ticks）或中文名（采集周期）"
            onChange={(e) => setKeyword(e.target.value)}
            data-testid="managed-editor-search"
          />
          {keyword ? (
            <button
              type="button"
              aria-label="清除搜索"
              className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              onClick={() => setKeyword('')}
              data-testid="managed-editor-search-clear"
            >
              <X aria-hidden className="size-3.5" />
            </button>
          ) : null}
        </div>

        <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
          <span data-testid="managed-editor-set-count">本层已设 {setCount} 项</span>
          {totalErrors > 0 ? (
            <span className="flex items-center gap-1 text-destructive" data-testid="managed-editor-error-count">
              <AlertTriangle aria-hidden className="size-3" />
              {totalErrors} 项不通过
            </span>
          ) : null}
        </div>
      </div>

      {view === 'yaml' ? (
        <ManagedYamlEditor
          items={items}
          value={yamlText}
          onChange={onYamlChange}
          onValidityChange={setYamlValid}
          onRegenerate={onRegenerateYaml}
        />
      ) : (
        <div className="flex flex-col gap-4 lg:flex-row">
          {/*
            分类导航：GitHub Settings 式的左侧竖列。
            - `lg` 以下改为顶部横向滚动条：手机/平板上竖列会挤掉表单宽度。
            - 每项用 `truncate` 收住长中文名，容器固定宽度，导航绝不压到右侧字段。
          */}
          {!query ? (
            <nav
              className="-mx-1 flex shrink-0 flex-row gap-1 overflow-x-auto px-1 pb-1 lg:mx-0 lg:w-52 lg:flex-col lg:overflow-visible lg:px-0 lg:pb-0"
              aria-label="配置分类"
              data-testid="managed-editor-nav"
            >
              {buckets.map((bucket) => {
                const active = bucket.group === currentGroup
                const count = setCountOf(bucket)
                const errs = errorCountOf(bucket)
                return (
                  <button
                    key={bucket.group}
                    type="button"
                    onClick={() => setActiveGroup(bucket.group)}
                    aria-current={active ? 'true' : undefined}
                    className={[
                      'flex shrink-0 items-center justify-between gap-2 overflow-hidden rounded-md px-2.5 py-1.5 text-left text-xs transition-colors lg:w-full',
                      active
                        ? 'bg-primary/10 font-medium text-primary'
                        : 'text-muted-foreground hover:bg-muted',
                    ].join(' ')}
                    data-testid={`managed-editor-nav-${bucket.group}`}
                  >
                    <span className="truncate">{bucket.label}</span>
                    <span className="flex shrink-0 items-center gap-1">
                      {errs > 0 ? (
                        <Badge variant="destructive" className="h-4 px-1 text-[10px] tabular-nums">
                          {errs}
                        </Badge>
                      ) : count > 0 ? (
                        <Badge variant="secondary" className="h-4 px-1 text-[10px] tabular-nums">
                          {count}
                        </Badge>
                      ) : null}
                      <span className="text-[10px] tabular-nums opacity-60">{bucket.items.length}</span>
                    </span>
                  </button>
                )
              })}
            </nav>
          ) : null}

          <div className="flex min-w-0 flex-1 flex-col">
            <div className="flex items-center gap-2 border-b pb-2">
              <h4 className="text-sm font-semibold">{query ? '搜索结果' : currentBucket?.label}</h4>
              <Badge variant="outline" className="text-[10px]">
                {visibleItems.length} 项
              </Badge>
              <span className="ml-auto flex items-center gap-1 text-[11px] text-muted-foreground">
                <Info aria-hidden className="size-3" />
                本层只保存你改过的键，没改的回退到低层——这是增量语义，不是覆盖。
              </span>
            </div>

            {visibleItems.length === 0 ? (
              <p className="mt-3 rounded-md border border-dashed px-3 py-6 text-center text-xs text-muted-foreground">
                没有匹配的配置项。试试键名片段（如 collect）或中文名（如 采集）。
              </p>
            ) : (
              // 单列行式：每项一行，行内自带控件+状态+写入，符合 GitHub Settings 的阅读顺序。
              <div className="flex flex-col" data-testid="managed-editor-fields">
                {visibleItems.map((meta) => (
                  <ManagedConfigField
                    key={meta.key}
                    meta={meta}
                    value={flat[meta.key]}
                    onChange={setKey}
                    validation={fieldValidations?.[meta.key]}
                    dirty={dirtyKeys?.has(meta.key)}
                    committing={committingKey === meta.key}
                    onCommit={onCommitKey ? () => onCommitKey(meta.key) : undefined}
                  />
                ))}
              </div>
            )}
          </div>
        </div>
      )}

      {inherited && Object.keys(inherited).length > 0 ? (
        <Alert data-testid="managed-editor-inherited">
          <Info aria-hidden />
          <AlertTitle>低层已有 {Object.keys(inherited).length} 个键</AlertTitle>
          <AlertDescription className="text-xs">
            这些键在你没有覆盖时按低层生效：{Object.keys(inherited).slice(0, 8).join('、')}
            {Object.keys(inherited).length > 8 ? ' 等' : ''}
          </AlertDescription>
        </Alert>
      ) : null}

      {/* YAML 始终可折叠展开：可视化视图下作为补充，YAML 视图下已在上方常驻。 */}
      {view === 'visual' ? (
        <Collapsible open={yamlOpen} onOpenChange={setYamlOpen}>
          <CollapsibleTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="justify-start gap-1 px-2 text-xs text-muted-foreground"
              data-testid="managed-editor-json-toggle"
            >
              <ChevronDown
                aria-hidden
                className={`size-3.5 transition-transform ${yamlOpen ? 'rotate-180' : ''}`}
              />
              高级：直接编辑 YAML
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="pt-1">
            {yamlOpen ? (
              <ManagedYamlEditor
                items={items}
                value={yamlText}
                onChange={onYamlChange}
                onValidityChange={setYamlValid}
                onRegenerate={onRegenerateYaml}
              />
            ) : null}
          </CollapsibleContent>
        </Collapsible>
      ) : null}

      {!yamlValid ? (
        <p className="text-xs text-destructive" data-testid="managed-editor-yaml-blocked">
          YAML 存在语法错误，修正后才能保存。
        </p>
      ) : null}
    </div>
  )
}
