import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { SegmentPills, type SegmentPillOption } from '@/components/views/console/SegmentPills'

/** 资源卡片的视图分段。 */
export type ResourceView = 'manage' | 'files' | 'browse'

const VIEWS: SegmentPillOption<ResourceView>[] = [
  { key: 'manage', labelKey: 'resourceCard.manage' },
  { key: 'files', labelKey: 'instanceDetail.files' },
  { key: 'browse', labelKey: 'resourceCard.browse' },
]

export interface InstanceResourceCardProps {
  /** 「管理」视图插槽（接收分段控件作 `toolbarLeading`）。 */
  renderManage: (leading: ReactNode) => ReactNode
  /** 「文件」视图插槽（接收分段控件作 `leading`）。 */
  renderFiles: (leading: ReactNode) => ReactNode
  /** 「浏览」视图插槽（接收顶栏分段控件作 `header`）。 */
  renderBrowse: (header: ReactNode) => ReactNode
}

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
 *
 * 分段控件渲染在**它所切换的那个视图内部**（寄居），切换会随旧视图卸载而带走焦点，
 * 故首次切换后每次重挂都把焦点送回当前段（`SegmentPills` 的 `autoFocusActive`）。
 */
export function InstanceResourceCard({ renderManage, renderFiles, renderBrowse }: InstanceResourceCardProps) {
  const { t } = useTranslation()
  const [view, setView] = useState<ResourceView>('manage')
  // 初始挂载不抢焦点，用户切过一次之后才接管。
  const [interacted, setInteracted] = useState(false)

  const segments = (
    <SegmentPills
      options={VIEWS}
      value={view}
      ariaLabel={t('resourceCard.viewGroup')}
      autoFocusActive={interacted}
      size="sm"
      className="p-0.5"
      onChange={(next) => {
        setInteracted(true)
        setView(next)
      }}
    />
  )

  return (
    // 非当前视图不挂载（与原 Radix TabsContent 默认行为一致，未改变生命周期语义）。
    <div className="flex h-full min-h-0 flex-col">
      {view === 'manage' && <div className="min-h-0 flex-1">{renderManage(segments)}</div>}
      {view === 'files' && <div className="min-h-0 flex-1">{renderFiles(segments)}</div>}
      {view === 'browse' && (
        <div className="min-h-0 flex-1">
          {/* FileBrowser 无横栏可蹭，故此视图仍用壳顶栏承载分段。 */}
          {renderBrowse(
            <div className="flex flex-none items-center border-b px-2 py-1">{segments}</div>,
          )}
        </div>
      )}
    </div>
  )
}
