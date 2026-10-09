import { describe, expect, it } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ReactNode } from 'react'
import { ProcessPanel } from '@/components/views/instances/ProcessPanel'

/**
 * FR-450 §2.3.1 进程指标面板 · 受控视图测（ADR-097 a 范式）。
 *
 * 本组件在应用侧原本没有测试，故这里是**新补**而非迁移。核心断言是**缺测不伪造**：
 * 探针不在时各项显「—」，绝不落 0 或 -1（FR-447 三态语义）。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        process: { title: '进程指标', cpu: 'CPU', memory: '内存', threads: '线程', uptime: '运行时长' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

function renderPanel(metrics?: Parameters<typeof ProcessPanel>[0]['metrics']) {
  function Wrapper({ children }: { children: ReactNode }) {
    return <I18nextProvider i18n={testI18n}>{children}</I18nextProvider>
  }
  return render(<ProcessPanel metrics={metrics} />, { wrapper: Wrapper })
}

/** 取某个统计卡的数值文本。 */
function statValue(label: string): string {
  const panel = screen.getByTestId('process-panel')
  const card = Array.from(panel.querySelectorAll('div.rounded-md')).find((d) => d.textContent?.startsWith(label))!
  return within(card as HTMLElement).getAllByText(/.+/).pop()!.textContent!.trim()
}

describe('ProcessPanel（FR-450 · ADR-097 a 范式）', () => {
  it('无指标时各项显「—」，不落 0 / -1 占位', () => {
    renderPanel(undefined)
    expect(statValue('CPU')).toBe('—')
    expect(statValue('内存')).toBe('—')
    expect(statValue('线程')).toBe('—')
    expect(statValue('运行时长')).toBe('—')
  })

  it('有 JVM 堆时内存显「已用 / 上限」', () => {
    renderPanel({ cpuPercent: 12.4, memoryMb: 1024, heapMaxMb: 2048, threads: 42, uptimeSeconds: 3660 })
    expect(statValue('CPU')).toBe('12%')
    expect(statValue('内存')).toBe('1.0G / 2.0G')
    expect(statValue('线程')).toBe('42')
    expect(statValue('运行时长')).toBe('1h 1m')
  })

  it('无 JVM 堆（原生二进制）时内存显 RSS', () => {
    renderPanel({ memoryMb: 512, heapMaxMb: 0, threads: 8 })
    expect(statValue('内存')).toBe('512M RSS')
  })

  it('线程数为 0 视为无数据（节点侧对非 JVM 进程的占位伪值）', () => {
    renderPanel({ cpuPercent: 5, memoryMb: 256, heapMaxMb: 0, threads: 0 })
    expect(statValue('线程')).toBe('—')
  })
})
