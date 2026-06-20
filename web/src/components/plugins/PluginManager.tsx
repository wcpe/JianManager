import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  usePlugins,
  useUploadPlugin,
  useDeletePlugin,
  useTogglePlugin,
  type PluginInfo,
} from '@/api/plugins'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import ConfirmDialog from '@/components/ConfirmDialog'

/** 插件/模组单服管理面板（FR-052）：列表 + 启用/禁用 + 上传 + 删除（二次确认）。 */
interface PluginManagerProps {
  /** 当前实例 id */
  instanceId: number
}

export default function PluginManager({ instanceId }: PluginManagerProps) {
  const { t } = useTranslation()
  const { data: plugins, isLoading, error } = usePlugins(instanceId)
  const upload = useUploadPlugin(instanceId)
  const toggle = useTogglePlugin(instanceId)
  const remove = useDeletePlugin(instanceId)

  const fileInputRef = useRef<HTMLInputElement>(null)
  // 上传目标目录：plugins（Bukkit 插件）或 mods（Forge/Fabric 模组）。
  const [uploadDir, setUploadDir] = useState('plugins')
  // 待删除插件（用于二次确认对话框）。
  const [deleteTarget, setDeleteTarget] = useState<PluginInfo | null>(null)

  const onPickFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (file) upload.mutate({ file, dir: uploadDir })
    if (fileInputRef.current) fileInputRef.current.value = ''
  }

  const confirmDelete = () => {
    if (deleteTarget) remove.mutate({ name: deleteTarget.name, dir: deleteTarget.dir })
    setDeleteTarget(null)
  }

  const formatSize = (bytes: number) => {
    if (bytes < 1024) return `${bytes} B`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  }

  return (
    <div className="space-y-4">
      {/* 工具栏：选择目标目录 + 上传 */}
      <div className="flex items-center gap-2">
        <h2 className="text-lg font-semibold">{t('plugins.title')}</h2>
        <div className="ml-auto flex items-center gap-2">
          <Select value={uploadDir} onValueChange={setUploadDir}>
            <SelectTrigger size="sm" className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="plugins">{t('plugins.dirPlugins')}</SelectItem>
              <SelectItem value="mods">{t('plugins.dirMods')}</SelectItem>
            </SelectContent>
          </Select>
          <Button size="sm" disabled={upload.isPending} onClick={() => fileInputRef.current?.click()}>
            {t('plugins.upload')}
          </Button>
          <input
            ref={fileInputRef}
            type="file"
            accept=".jar"
            className="hidden"
            onChange={onPickFile}
          />
        </div>
      </div>

      {isLoading ? (
        <p className="text-sm text-muted-foreground">{t('plugins.loading')}</p>
      ) : error ? (
        <p className="text-sm text-destructive">
          {(error as Error & { response?: { data?: { message?: string } } }).response?.data?.message ||
            t('plugins.loadFailed')}
        </p>
      ) : !plugins || plugins.length === 0 ? (
        <p className="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          {t('plugins.empty')}
        </p>
      ) : (
        <Table>
          <TableHeader className="bg-muted/50">
            <TableRow>
              <TableHead>{t('plugins.name')}</TableHead>
              <TableHead className="w-24">{t('plugins.dir')}</TableHead>
              <TableHead className="w-24">{t('plugins.status')}</TableHead>
              <TableHead className="w-24 text-right">{t('plugins.size')}</TableHead>
              <TableHead className="w-44 text-right">{t('plugins.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {plugins.map((p) => (
              <TableRow key={`${p.dir}/${p.name}`}>
                <TableCell className="font-mono text-xs">{p.name}</TableCell>
                <TableCell>
                  <Badge variant="outline">
                    {p.dir === 'mods' ? t('plugins.dirMods') : t('plugins.dirPlugins')}
                  </Badge>
                </TableCell>
                <TableCell>
                  {p.enabled ? (
                    <Badge className="bg-emerald-500/15 text-emerald-600 dark:text-emerald-400">
                      {t('plugins.statusEnabled')}
                    </Badge>
                  ) : (
                    <Badge variant="secondary">{t('plugins.statusDisabled')}</Badge>
                  )}
                </TableCell>
                <TableCell className="text-right tabular-nums text-xs text-muted-foreground">
                  {formatSize(p.size)}
                </TableCell>
                <TableCell className="text-right">
                  <div className="flex justify-end gap-1.5">
                    <Button
                      size="xs"
                      variant="outline"
                      disabled={toggle.isPending}
                      onClick={() => toggle.mutate({ name: p.name, dir: p.dir })}
                    >
                      {p.enabled ? t('plugins.disable') : t('plugins.enable')}
                    </Button>
                    <Button
                      size="xs"
                      variant="ghost"
                      className="text-destructive hover:text-destructive"
                      disabled={remove.isPending}
                      onClick={() => setDeleteTarget(p)}
                    >
                      {t('plugins.delete')}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <ConfirmDialog
        open={!!deleteTarget}
        title={t('plugins.deleteTitle')}
        description={t('plugins.deleteConfirm', { name: deleteTarget?.name ?? '' })}
        confirmLabel={t('plugins.delete')}
        variant="destructive"
        onConfirm={confirmDelete}
        onCancel={() => setDeleteTarget(null)}
      />
    </div>
  )
}
