import { useCallback, useEffect, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Clapperboard, Pause, Play, SkipForward } from 'lucide-react'
import { useDirectorStore } from '@/stores/director'
import { useWorkspaceStore } from '@/stores/workspace'
import { sceneStatus } from '@/lib/director'
import { cn } from '@/lib/utils'
import DirectorCanvas from './DirectorCanvas'
import DirectorSceneStrip from './DirectorSceneStrip'
import DirectorAddSceneMenu from './DirectorAddSceneMenu'

/**
 * 工作区导播台页面（FR-168 / ADR-035 / design §9）。
 *
 * OBS 式多场景瞬切：缩略图条选场景，点击 / 数字键 / 方向键**瞬切**，可定时轮播。
 * 核心是 ADR-035 的预热并发模型——
 * - **预热场景的画布全部同时挂载**（故其卡的 WS 保活），但只有 active 场景全速渲染 + 可见；
 *   非激活场景由 {@link DirectorCanvas} 用 `content-visibility` + 终端暂停 buffer 降频/暂停重绘。
 * - **cold 场景不挂载**（无 WS）；切到才挂载并由状态机加入保活池（受并发上限 + LRU 约束）。
 *
 * 状态机纯逻辑见 `lib/director`；保活/节流副作用在这里 + `DirectorCanvas` + `Terminal`。
 */
export default function DirectorConsolePage() {
  const { t } = useTranslation()

  const scenes = useDirectorStore((s) => s.scenes)
  const machine = useDirectorStore((s) => s.machine)
  const carouselOn = useDirectorStore((s) => s.carouselOn)
  const carouselMs = useDirectorStore((s) => s.carouselMs)
  const activate = useDirectorStore((s) => s.activate)
  const advance = useDirectorStore((s) => s.advance)
  const setCarouselOn = useDirectorStore((s) => s.setCarouselOn)

  const userPresets = useWorkspaceStore((s) => s.userPresets)

  const activeId = machine.activeId

  // 首次进入 / 仅一个场景时自动激活第一个，免去用户手点。
  useEffect(() => {
    if (activeId === null && scenes.length > 0) activate(scenes[0].id)
  }, [activeId, scenes, activate])

  // 只挂载预热（含 active）场景的画布；cold 场景不挂载（不建 WS）。
  const mountedScenes = useMemo(
    () => scenes.filter((s) => sceneStatus(machine, s.id) !== 'cold'),
    [scenes, machine],
  )

  // —— 轮播：定时按序瞬切（复用瞬切逻辑）。仅 active 存在且场景 ≥2 时有意义。 ——
  // 用「最新值 ref」让 interval 回调取到最新 advance，又不因 advance 变化重建 interval。
  const advanceRef = useRef(advance)
  useEffect(() => {
    advanceRef.current = advance
  }, [advance])
  useEffect(() => {
    if (!carouselOn || scenes.length < 2) return
    const id = window.setInterval(() => advanceRef.current(), carouselMs)
    return () => window.clearInterval(id)
  }, [carouselOn, carouselMs, scenes.length])

  // —— 快捷键：数字 1-9 瞬切到第 N 个场景；← / → 上/下一个。 ——
  const activateByIndex = useCallback(
    (idx: number) => {
      const s = scenes[idx]
      if (s) activate(s.id)
    },
    [scenes, activate],
  )
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // 不抢输入焦点：在输入框/终端里打字不触发瞬切。
      const el = e.target as HTMLElement | null
      if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return
      if (e.altKey || e.ctrlKey || e.metaKey) return
      if (e.key >= '1' && e.key <= '9') {
        activateByIndex(Number(e.key) - 1)
      } else if (e.key === 'ArrowRight') {
        advanceRef.current()
      } else if (e.key === 'ArrowLeft') {
        // 上一个：环绕到前一个场景。
        const idx = activeId ? scenes.findIndex((s) => s.id === activeId) : 0
        const prev = (idx - 1 + scenes.length) % Math.max(1, scenes.length)
        activateByIndex(prev)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [activateByIndex, activeId, scenes])

  const canCarousel = scenes.length >= 2

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* 顶部：标题 + 轮播控制 + 添加场景 */}
      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b px-3 py-2">
        <div className="flex items-center gap-1.5 text-sm">
          <Clapperboard className="size-4 text-primary" />
          <span className="text-muted-foreground">{t('nav.cluster')}</span>
          <span className="text-muted-foreground">/</span>
          <span className="font-medium">{t('director.title')}</span>
        </div>

        <div className="ml-auto flex items-center gap-1.5">
          <button
            type="button"
            disabled={!canCarousel}
            onClick={() => setCarouselOn(!carouselOn)}
            title={carouselOn ? t('director.carouselStop') : t('director.carouselStart')}
            className={cn(
              'flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-40',
              carouselOn
                ? 'border-primary/40 bg-primary/10 text-primary'
                : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
            )}
          >
            {carouselOn ? <Pause className="size-3.5" /> : <Play className="size-3.5" />}
            {carouselOn ? t('director.carouselOn') : t('director.carousel')}
          </button>
          <button
            type="button"
            disabled={scenes.length < 2}
            onClick={() => advance()}
            title={t('director.next')}
            aria-label={t('director.next')}
            className="flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
          >
            <SkipForward className="size-4" />
          </button>
          <DirectorAddSceneMenu userPresets={userPresets} />
        </div>
      </div>

      {scenes.length === 0 ? (
        <DirectorEmpty userPresets={userPresets} />
      ) : (
        <>
          {/* 主舞台：所有预热场景叠放，仅 active 可见；非激活保活 + 暂停渲染。 */}
          <div className="relative min-h-0 flex-1 overflow-hidden bg-muted/20">
            {mountedScenes.map((scene) => (
              <DirectorCanvas key={scene.id} cards={scene.cards} active={scene.id === activeId} />
            ))}
          </div>

          {/* 缩略图条：一排场景，点击瞬切；显三态 + 序号快捷键。 */}
          <DirectorSceneStrip />
        </>
      )}
    </div>
  )
}

/** 空态：引导从已保存的（超级工作台）预设导入为场景。 */
function DirectorEmpty({ userPresets }: { userPresets: ReturnType<typeof useWorkspaceStore.getState>['userPresets'] }) {
  const { t } = useTranslation()
  return (
    <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 p-6 text-center">
      <Clapperboard className="size-9 text-muted-foreground/40" />
      <p className="text-sm font-medium text-muted-foreground">{t('director.emptyTitle')}</p>
      <p className="max-w-md text-xs text-muted-foreground/70">
        {userPresets.length === 0 ? t('director.emptyNoPresets') : t('director.emptyHint')}
      </p>
      {userPresets.length > 0 && (
        <div className="mt-1">
          <DirectorAddSceneMenu userPresets={userPresets} variant="primary" />
        </div>
      )}
    </div>
  )
}
