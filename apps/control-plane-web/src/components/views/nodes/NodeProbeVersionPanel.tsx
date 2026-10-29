import { useTranslation } from 'react-i18next'
import { NativeSelect } from '@jianmanager/ui/components/native-select'
import { Skeleton } from '@jianmanager/ui/components/skeleton'
import { Button } from '@jianmanager/ui/components/button'

/** 
 * 可选探针版本（本组件所需的最小结构）。
 *
 * 刻意只声明用到的三个字段，而非照搬 API 层的 `ArtifactVersion`：外壳直接传完整的
 * `ServerProbeCatalog` 也结构兼容，从而无需把制品库的整套类型迁进包（ADR-103 的
 * 「契约类型归包」针对的是**确实被两侧共用**的类型；这里组件只消费一个子集）。
 */
export interface ProbeVersionOption {
  id: number
  version: string
  assetId: number
}

/** 探针目录视图（同样只声明本组件用到的字段）。 */
export interface ProbeCatalogView {
  versions: ProbeVersionOption[]
  package: { defaultVersionId: number }
}

/** 
 * 节点默认探针版本：Worker 默认版本仅用于之后新建的实例，不会自动改动已有实例。
 *
 * 受控视图（ADR-103 b 范式）：不取数、不发请求、不弹 toast——目录与当前选择经 props 注入，
 * 保存经 `onSave` 上报由外壳执行。
 */
export interface NodeProbeVersionPanelProps {
  /** 制品目录（含可选版本与默认版本 id）；外壳取数后注入。 */
  catalog?: ProbeCatalogView
  /** 当前选中的版本 id；0 表示继承节点默认。 */
  versionId?: number
  /** 加载态（目录或选择任一在途）。 */
  isLoading?: boolean
  /** 错误态（目录或选择任一失败）。 */
  isError?: boolean
  /** 保存在途：禁用选择框。 */
  saving?: boolean
  /** 保存选择。返回是否成功（组件不关心后续提示）。 */
  onSave: (versionId: number) => Promise<boolean>
  /** 取数失败时的重试（外壳注入）。 */
  onRetry?: () => void
}

export default function NodeProbeVersionPanel({
  catalog,
  versionId = 0,
  isLoading,
  isError,
  saving = false,
  onSave,
  onRetry,
}: NodeProbeVersionPanelProps) {
  const { t } = useTranslation()
  // 仅列出已落制品库的版本（assetId > 0），未上传的不可选。
  const versions = (catalog?.versions ?? []).filter((version) => version.assetId > 0)

  // 骨架而非一行文字：本页是下拉选择，加载时先占位能避免选中后布局跳动。
  if (isLoading)
    return (
      <div className="space-y-2">
        <Skeleton className="h-3.5 w-48" />
        <Skeleton className="h-9 w-full max-w-md" />
      </div>
    )
  if (isError || !catalog)
    return (
      /* 
        只给一行红字时，用户唯一的自救手段是刷新整页（连当前节点与页签一起丢）。
        这里给重试按钮，把「失败」变成一个可恢复的动作。
      */
      <div className="flex flex-col items-start gap-2" data-testid="probe-version-error">
        <p className="text-sm text-destructive">{t('probe.nodeLoadFailed')}</p>
        {onRetry && (
          <Button variant="outline" size="sm" onClick={onRetry}>
            {t('common.retry')}
          </Button>
        )}
      </div>
    )

  return (
    <div className="space-y-3">
      {/* 标题由外层 Panel 提供，这里只留说明，避免同一句话出现两遍。 */}
      <p className="text-xs text-muted-foreground">{t('probe.nodeVersionHint')}</p>
      {versions.length === 0 && (
        // 没有已落库的版本时只有「继承」一项可选：说清楚「要先去制品库上传」，否则看着像下拉坏了。
        <p className="rounded-md border border-dashed px-2.5 py-2 text-[11px] text-muted-foreground">
          {t('probe.nodeNoVersions')}
        </p>
      )}
      {/* 版本候选来自后端探针版本目录（数量随升级累积），不用 Radix Select 全量挂载 → NativeSelect */}
      <NativeSelect
        className="w-full max-w-md"
        value={String(versionId)}
        disabled={saving}
        onChange={(event) => {
          void onSave(Number(event.target.value))
        }}
      >
        <option value="0">{t('probe.nodeInherit', { version: catalog.versions.find((version) => version.id === catalog.package.defaultVersionId)?.version || '—' })}</option>
        {versions.map((version) => <option key={version.id} value={version.id}>{version.version}</option>)}
      </NativeSelect>
    </div>
  )
}
