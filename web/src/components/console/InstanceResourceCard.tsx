import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Download } from 'lucide-react'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import ConfigExplorer from '@/components/config-explorer/ConfigExplorer'
import FileBrowser, { type FileBrowserAction } from '@/components/file-browser/FileBrowser'
import { instanceFileSource } from '@/components/file-browser/sources/instanceSource'

/**
 * 实例「资源卡片」（FR-130 文件+配置合一；FR-213 迁移到共享文件浏览器）。
 *
 * 两个视图，能力一个不少：
 * - 「管理」= 既有全功能资源/配置管理器（{@link ConfigExplorer}：增删改查 / 上传 / 文本与配置编辑 /
 *   配置版本 / 跨文件校验 / 收藏 / 已发现配置 / 搜索 / 归档浏览 / 反编译）——**行为不变，不删任何能力**。
 * - 「浏览」= 共享只读文件浏览器（{@link FileBrowser} + 实例数据源适配器）：目录树 + 内容预览
 *   （文本/配置/json 高亮，二进制/超大降级）+ 下载。这是 FR-213 抽取的共享组件，
 *   与客户端分发文件预览（FR-214）同源。
 */
interface InstanceResourceCardProps {
  instanceId: number
}

export default function InstanceResourceCard({ instanceId }: InstanceResourceCardProps) {
  const { t } = useTranslation()
  const source = useMemo(() => instanceFileSource(instanceId), [instanceId])

  // 浏览态注入「下载」行操作（共享组件不内置写端点，下载经数据源回调）。
  const actions = useMemo<FileBrowserAction[]>(
    () => [
      {
        key: 'download',
        label: t('fileBrowser.download'),
        icon: <Download className="size-4" />,
        visible: (e) => !e.isDir,
        onAction: (e) => {
          void source.download?.(e)
        },
      },
    ],
    [t, source],
  )

  return (
    <Tabs defaultValue="manage" className="flex h-full min-h-0 flex-col">
      <TabsList className="self-start rounded-full">
        <TabsTrigger value="manage" className="rounded-full text-xs">
          {t('resourceCard.manage')}
        </TabsTrigger>
        <TabsTrigger value="browse" className="rounded-full text-xs">
          {t('resourceCard.browse')}
        </TabsTrigger>
      </TabsList>
      <TabsContent value="manage" className="mt-3 min-h-0 flex-1">
        <ConfigExplorer instanceId={instanceId} />
      </TabsContent>
      <TabsContent value="browse" className="mt-3 min-h-0 flex-1">
        <FileBrowser source={source} readOnly={false} actions={actions} />
      </TabsContent>
    </Tabs>
  )
}
