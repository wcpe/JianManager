import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Download } from 'lucide-react'
import { cn } from '@jianmanager/ui'
import ConfigExplorer from '@/components/config-explorer/ConfigExplorer'
import UnifiedExplorerShell from '@/components/file-browser/UnifiedExplorerShell'
import {
  instanceBrowseCapability,
  instanceFilesCapability,
} from '@/components/file-browser/capability'
import { instanceFileSource } from '@/components/file-browser/sources/instanceSource'
import type { FileBrowserAction } from '@/components/file-browser/types'

/**
 * 实例「资源卡片」（FR-130 文件+配置合一；FR-213 共享浏览器；FR-378 统一壳）。
 *
 * - 「管理」= ConfigExplorer（全功能配置，能力不减）
 * - 「文件」= UnifiedExplorerShell + instance-files Capability → ExplorerTabHost
 * - 「浏览」= UnifiedExplorerShell + instance-browse → FileBrowser + 下载
 *
 * FR-422：分段切换**不再自成一条横栏**。它下沉为各视图内层管理器已有横栏的左端插槽——
 * 管理视图进资源管理器工具栏、文件视图进多标签的标签条；只有浏览视图（FileBrowser 是
 * 树｜预览两栏、没有横栏可蹭）仍需一条自己的头栏。原先「分段栏 + 工具栏三条」共四条横栏，
 * 管理视图现在压到一条。
 */
interface InstanceResourceCardProps {
  instanceId: number
}

type ResourceView = 'manage' | 'files' | 'browse'

const VIEWS: Array<{ key: ResourceView; labelKey: string }> = [
  { key: 'manage', labelKey: 'resourceCard.manage' },
  { key: 'files', labelKey: 'instanceDetail.files' },
  { key: 'browse', labelKey: 'resourceCard.browse' },
]

/**
 * 视图分段控件（FR-422）。
 *
 * 用 `aria-pressed` 的按钮组而非 ARIA tablist：本控件渲染在**它所切换的那个视图内部**
 * （寄居于内层管理器的横栏），tablist 嵌在 tabpanel 里是无效结构。形态与 FR-413 的
 * `InstanceResourceSegment` 一致（同为 pill 按钮组 + aria-pressed）。
 *
 * `autoFocusActive`：因寄居，切换会随旧视图卸载而带走焦点。首次切换后每次重挂都把焦点
 * 送回当前段按钮，键盘用户可连续按 Tab/Enter 换段而不掉到 body。
 */
function ViewSegments({
  value,
  onChange,
  autoFocusActive,
}: {
  value: ResourceView
  onChange: (view: ResourceView) => void
  autoFocusActive: boolean
}) {
  const { t } = useTranslation()
  const activeRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (autoFocusActive) activeRef.current?.focus()
  }, [autoFocusActive, value])

  return (
    <div
      role="group"
      aria-label={t('resourceCard.viewGroup')}
      className="inline-flex shrink-0 rounded-full bg-muted p-0.5"
    >
      {VIEWS.map(({ key, labelKey }) => (
        <button
          key={key}
          ref={key === value ? activeRef : undefined}
          type="button"
          onClick={() => onChange(key)}
          aria-pressed={value === key}
          className={cn(
            'rounded-full px-2.5 py-0.5 text-xs transition-colors',
            value === key
              ? 'bg-card font-semibold text-foreground shadow-soft'
              : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {t(labelKey)}
        </button>
      ))}
    </div>
  )
}

export default function InstanceResourceCard({ instanceId }: InstanceResourceCardProps) {
  const { t } = useTranslation()
  const source = useMemo(() => instanceFileSource(instanceId), [instanceId])
  const [view, setView] = useState<ResourceView>('manage')
  // 初始挂载不抢焦点，用户切过一次之后才接管（见 ViewSegments 注释）。
  const [interacted, setInteracted] = useState(false)

  const downloadAction = useMemo<FileBrowserAction>(
    () => ({
      key: 'download',
      label: t('fileBrowser.download'),
      icon: <Download className="size-4" />,
      visible: (e) => !e.isDir,
      onAction: (e) => {
        void source.download?.(e)
      },
    }),
    [t, source],
  )

  const filesCap = useMemo(() => instanceFilesCapability(), [])
  const browseCap = useMemo(() => instanceBrowseCapability(downloadAction), [downloadAction])

  const segments = (
    <ViewSegments
      value={view}
      autoFocusActive={interacted}
      onChange={(next) => {
        setInteracted(true)
        setView(next)
      }}
    />
  )

  return (
    // 非当前视图不挂载（与原 Radix TabsContent 默认行为一致，未改变生命周期语义）。
    <div className="flex h-full min-h-0 flex-col">
      {view === 'manage' && (
        <div className="min-h-0 flex-1">
          <ConfigExplorer instanceId={instanceId} toolbarLeading={segments} />
        </div>
      )}
      {view === 'files' && (
        <div className="min-h-0 flex-1">
          <UnifiedExplorerShell capability={filesCap} instanceId={instanceId} leading={segments} />
        </div>
      )}
      {view === 'browse' && (
        <div className="min-h-0 flex-1">
          {/* FileBrowser 无横栏可蹭，故此视图仍用壳顶栏承载分段。 */}
          <UnifiedExplorerShell
            capability={browseCap}
            source={source}
            header={<div className="flex flex-none items-center border-b px-2 py-1">{segments}</div>}
          />
        </div>
      )}
    </div>
  )
}
