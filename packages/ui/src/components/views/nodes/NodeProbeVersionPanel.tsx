import { useTranslation } from 'react-i18next'

/**
 * 可选探针版本（本组件所需的最小结构）。
 *
 * 刻意只声明用到的三个字段，而非照搬 API 层的 `ArtifactVersion`：外壳直接传完整的
 * `ServerProbeCatalog` 也结构兼容，从而无需把制品库的整套类型迁进包（ADR-097 的
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
 * 节点默认探针版本（FR-411）：Worker 默认版本仅用于之后新建的实例，不会自动改动已有实例。
 *
 * 受控视图（ADR-097 b 范式）：不取数、不发请求、不弹 toast——目录与当前选择经 props 注入，
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
}

export default function NodeProbeVersionPanel({
  catalog,
  versionId = 0,
  isLoading,
  isError,
  saving = false,
  onSave,
}: NodeProbeVersionPanelProps) {
  const { t } = useTranslation()
  // 仅列出已落制品库的版本（assetId > 0），未上传的不可选。
  const versions = (catalog?.versions ?? []).filter((version) => version.assetId > 0)

  if (isLoading) return <p className="text-sm text-muted-foreground">{t('common.loading')}</p>
  if (isError || !catalog) return <p className="text-sm text-destructive">{t('probe.nodeLoadFailed')}</p>

  return (
    <div className="space-y-3">
      <div>
        <h3 className="text-sm font-semibold">{t('probe.nodeVersionTitle')}</h3>
        <p className="mt-1 text-xs text-muted-foreground">{t('probe.nodeVersionHint')}</p>
      </div>
      <select
        className="h-9 w-full max-w-md rounded-md border bg-background px-2 text-sm"
        value={String(versionId)}
        disabled={saving}
        onChange={(event) => {
          void onSave(Number(event.target.value))
        }}
      >
        <option value="0">{t('probe.nodeInherit', { version: catalog.versions.find((version) => version.id === catalog.package.defaultVersionId)?.version || '—' })}</option>
        {versions.map((version) => <option key={version.id} value={version.id}>{version.version}</option>)}
      </select>
    </div>
  )
}
