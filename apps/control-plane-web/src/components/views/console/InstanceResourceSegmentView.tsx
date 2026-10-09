/**
 * @file InstanceResourceSegmentView：文件配置页签的分段壳（分段切换 + 三段保活）的受控视图，三段内容经插槽注入。
 * @input lib/instance-console-tabs（ResourceSegment）、views/console/SegmentPills（分段控件，收敛原手写药丸栏）、cn
 * @output InstanceResourceSegmentView、InstanceResourceSegmentViewProps
 * @sync apps/control-plane-web/src/components/console/InstanceResourceSegment.tsx、
 *        apps/control-plane-web/src/components/console/InstanceConsolePage.tsx
 * @since FR-502（组件受控化迁包；原 FR-413 页签内分段、FR-451 关键配置段）
 */
import { Activity, type ReactNode } from 'react'
import { cn } from '@jianmanager/ui'
import { SegmentPills, type SegmentPillOption } from '@/components/views/console/SegmentPills'
import type { ResourceSegment } from '@jianmanager/ui/lib/instance-console-tabs'

// 分段标签用「文件」而非「文件配置」——后者是本页签自己的名字，同名会撞可访问性名称。
const SEGMENTS: SegmentPillOption<ResourceSegment>[] = [
  { key: 'config', labelKey: 'serverConsole.segConfig' },
  { key: 'files', labelKey: 'serverConsole.segFiles' },
  { key: 'env', labelKey: 'serverConsole.env' },
]

/**
 * 受控边界（ADR-097 b 范式）：**不取数、不发请求、不弹 toast**。
 * - 分段值 `segment` 归容器（它与深链 `?seg=` / `?tab=env` 解析结果同源，且决定三段各挂哪个视图）；
 *   本视图只渲染分段控件并回传切换；
 * - 三段内容走 `configContent`/`filesContent`/`envContent` 插槽：三段本体是各自取数的视图接线层
 *   （关键配置面板 / 文件管理器 / 环境变量编辑器），「何时取数、取什么」属应用侧策略，
 *   故由容器注入而不是在包内取；
 * - 保活（`<Activity>`）与外壳布局留本视图：它们是与数据无关的呈现细节。
 *
 * 分段控件复用 `SegmentPills`（属既有的重复实现收敛：原先本处手写一份 `rounded-full` 药丸按钮组，
 * 与 FR-422 资源卡片视图分段重复）。`autoFocusActive` 不启用——本分段栏自成一栏、不寄居在
 * 被切换视图内部，切换不会带走焦点，无需回送焦点。
 */
export interface InstanceResourceSegmentViewProps {
  /** 当前分段。 */
  segment: ResourceSegment
  /** 分段切换上报。 */
  onSegmentChange: (segment: ResourceSegment) => void
  /** 关键配置段内容（容器注入取数后的面板）。 */
  configContent: ReactNode
  /** 文件段内容（容器注入取数后的文件管理器）。 */
  filesContent: ReactNode
  /** 环境变量段内容（容器注入取数后的编辑器）。 */
  envContent: ReactNode
}

/**
 * 文件配置页签（FR-413）：把原独立的「环境变量」页签并入本页签的一个分段——
 * 环境变量本质是工作目录下的 `.env`，独占一个顶级页签既挤 Tab 栏又割裂心智模型。
 *
 * 三个分段都保活（`<Activity>`）：环境变量编辑器持未保存草稿，分段来回切不得丢；
 * 文件管理器的目录展开态与选中集同理。与页签级 keep-alive（FR-295）同一手法。
 */
export default function InstanceResourceSegmentView({
  segment,
  onSegmentChange,
  configContent,
  filesContent,
  envContent,
}: InstanceResourceSegmentViewProps) {
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card shadow-soft">
      <div className="flex flex-none items-center gap-1 border-b px-2 py-1.5">
        {/* 不传 ariaLabel：分组名若用「文件配置」会与本页签（tabpanel）的名字撞可访问性名称——
            原实现刻意只用「文件」区分段名；各段按钮自带可见文案 + aria-pressed，无需额外命名。 */}
        <SegmentPills options={SEGMENTS} value={segment} onChange={onSegmentChange} />
      </div>

      {/* 关键配置段（FR-451）：启动参数 + server.properties 关键项明面化，在此层滚。 */}
      <Activity mode={segment === 'config' ? 'visible' : 'hidden'}>
        <div className={cn('flex min-h-0 flex-col overflow-auto', segment === 'config' ? 'flex-1' : 'hidden')}>
          {configContent}
        </div>
      </Activity>

      {/* 文件段：管理器内部自己收口滚动，故此层 overflow-hidden。 */}
      <Activity mode={segment === 'files' ? 'visible' : 'hidden'}>
        <div className={cn('flex min-h-0 flex-col overflow-hidden', segment === 'files' ? 'flex-1' : 'hidden')}>
          {filesContent}
        </div>
      </Activity>

      {/* 环境变量段：纵向堆叠两个面板，在此层滚。 */}
      <Activity mode={segment === 'env' ? 'visible' : 'hidden'}>
        <div className={cn('flex min-h-0 flex-col overflow-auto', segment === 'env' ? 'flex-1' : 'hidden')}>
          {envContent}
        </div>
      </Activity>
    </div>
  )
}
