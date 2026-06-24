import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Copy, Download } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useUpdaterJarsInfo, downloadUpdaterJar } from '@/api/clientChannels'

/**
 * 客户端更新器接入指引（FR-107）。面向运营方：在频道详情一页拿齐——下载更新器两件套、
 * 该频道专属 jm-updater.json、启动器 JVM 参数、行为说明，照做即可把 OTA 更新器接入并下发玩家。
 */
export default function ClientIntegrationGuide({ channelId }: { channelId: string }) {
  const { t } = useTranslation()
  const { data: jars } = useUpdaterJarsInfo()
  const [endpoint, setEndpoint] = useState(`${window.location.origin}/api/v1`)
  const [downloading, setDownloading] = useState<'wedge' | 'core' | null>(null)

  const jmUpdaterJson = JSON.stringify(
    {
      channel: channelId,
      key: t('clientGuide.keyPlaceholder', '在「拉取密钥」Tab 创建后填入'),
      endpoint,
      coreJar: 'updater-core.jar',
      timeoutSec: 120,
      telemetry: true,
      bootConfirmSec: 5,
      coreVersion: 0,
    },
    null,
    2,
  )
  const javaagentArg = '-javaagent:jm-updater\\wedge.jar'

  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast.success(t('clientGuide.copied', '已复制'))
    } catch {
      toast.error(t('clientGuide.copyFailed', '复制失败'))
    }
  }

  const download = async (comp: 'wedge' | 'core') => {
    setDownloading(comp)
    try {
      await downloadUpdaterJar(comp)
    } catch {
      toast.error(t('clientGuide.downloadFailed', '下载失败（jar 可能未内嵌）'))
    } finally {
      setDownloading(null)
    }
  }

  return (
    <div className="space-y-6 text-sm">
      {/* 简介 + 内嵌版本 */}
      <div className="border rounded-lg p-4 space-y-2">
        <p className="text-muted-foreground">
          {t(
            'clientGuide.intro',
            '楔子在游戏启动前自定位、加载 updater-core、拉签名 manifest、增量更新客户端资源后放行游戏；断网或配置异常一律放行（fail-static / fail-open），绝不挡启动。按下面步骤把更新器接入整合包并下发玩家。',
          )}
        </p>
        {jars && (
          <p className="text-xs text-muted-foreground">
            {t('clientGuide.embeddedVersion', '内嵌更新器版本')}:{' '}
            <span className="font-mono">{jars.version}</span>
          </p>
        )}
      </div>

      {/* 步骤一：下载两件套 */}
      <Step title={t('clientGuide.step1Title', '① 下载更新器两件套')}>
        <p className="text-muted-foreground">
          {t('clientGuide.step1Desc', '下载 wedge.jar（楔子）与 updater-core.jar（更新核心）。')}
        </p>
        <div className="flex flex-wrap gap-2 mt-2">
          <Button
            variant="outline"
            size="sm"
            disabled={!jars?.wedge.available || downloading === 'wedge'}
            onClick={() => download('wedge')}
          >
            <Download className="size-4 mr-1" /> wedge.jar
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={!jars?.core.available || downloading === 'core'}
            onClick={() => download('core')}
          >
            <Download className="size-4 mr-1" /> updater-core.jar
          </Button>
          {jars && (!jars.wedge.available || !jars.core.available) && (
            <span className="text-xs text-amber-600 self-center">
              {t('clientGuide.notEmbedded', '部分 jar 未内嵌（构建时需 make embed-client-updater）')}
            </span>
          )}
        </div>
      </Step>

      {/* 步骤二：放置文件 */}
      <Step title={t('clientGuide.step2Title', '② 放置文件')}>
        <p className="text-muted-foreground">
          {t('clientGuide.step2Desc', '把两个 jar 与 jm-updater.json 放进游戏目录的 jm-updater 子目录：')}
        </p>
        <CodeBlock text={'<.minecraft>/jm-updater/\n  ├─ wedge.jar\n  ├─ updater-core.jar\n  └─ jm-updater.json'} onCopy={copy} t={t} />
      </Step>

      {/* 步骤三：频道专属 jm-updater.json */}
      <Step title={t('clientGuide.step3Title', '③ 配置 jm-updater.json（本频道专属）')}>
        <p className="text-muted-foreground">
          {t(
            'clientGuide.step3Desc',
            '下面是本频道专属配置。key 换成你在「拉取密钥」Tab 创建的密钥（明文仅创建时一次性显示）；endpoint 改成玩家可访问的公网分发地址。',
          )}
        </p>
        <label className="flex flex-col gap-1 mt-2">
          <span className="text-xs text-muted-foreground">{t('clientGuide.endpointLabel', '公网分发端点')}</span>
          <input
            className="border rounded px-2 py-1 text-sm font-mono bg-background"
            value={endpoint}
            onChange={(e) => setEndpoint(e.target.value)}
          />
        </label>
        <CodeBlock text={jmUpdaterJson} onCopy={copy} t={t} />
      </Step>

      {/* 步骤四：启动器 JVM 参数 */}
      <Step title={t('clientGuide.step4Title', '④ 启动器加 JVM 参数')}>
        <p className="text-muted-foreground">
          {t(
            'clientGuide.step4Desc',
            '在 HMCL / PCL2 的「自定义 JVM 参数」里加下面这行（相对路径，推荐）。启动器启动游戏前会切到 .minecraft 目录，故相对路径相对游戏目录解析；省略 =gameDir 让楔子自动从 MC 命令行 --gameDir 取绝对路径。',
          )}
        </p>
        <CodeBlock text={javaagentArg} onCopy={copy} t={t} />
        <p className="text-xs text-muted-foreground">
          {t('clientGuide.step4Abs', '若启动器不切到 .minecraft，改用绝对路径：')}
          <span className="font-mono"> -javaagent:&lt;.minecraft&gt;\jm-updater\wedge.jar</span>
        </p>
      </Step>

      {/* 行为说明 */}
      <Step title={t('clientGuide.behaviorTitle', '行为说明')}>
        <ul className="list-disc pl-5 space-y-1 text-muted-foreground">
          <li>{t('clientGuide.behaviorFailStatic', '断网 / 端点不可达 → 以本地现有版本启动游戏（fail-static）。')}</li>
          <li>{t('clientGuide.behaviorFailOpen', '配置缺失 / 异常 → 跳过更新直接启动（fail-open）。绝不挡启动。')}</li>
          <li>{t('clientGuide.behaviorProgress', '更新期弹独立进度窗口（进度条 + 速度 + ETA）；无显示环境降级为文本。')}</li>
          <li>{t('clientGuide.behaviorMultiAgent', '可与外置登录 authlib-injector 等其它 -javaagent 共存，加载顺序无关。')}</li>
        </ul>
      </Step>
    </div>
  )
}

/** 步骤区块：标题 + 内容。 */
function Step({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="border rounded-lg p-4 space-y-2">
      <h3 className="font-medium">{title}</h3>
      {children}
    </div>
  )
}

/** 等宽代码块 + 复制按钮。 */
function CodeBlock({
  text,
  onCopy,
  t,
}: {
  text: string
  onCopy: (text: string) => void
  t: (key: string, fallback: string) => string
}) {
  return (
    <div className="relative mt-2">
      <pre className="bg-muted rounded-lg p-3 pr-12 text-xs font-mono whitespace-pre-wrap break-all overflow-x-auto">
        {text}
      </pre>
      <Button
        variant="ghost"
        size="sm"
        className="absolute top-2 right-2 h-7 px-2"
        onClick={() => onCopy(text)}
        aria-label={t('clientGuide.copy', '复制')}
      >
        <Copy className="size-3.5" />
      </Button>
    </div>
  )
}
