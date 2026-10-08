/**
 * 博物馆的 i18n 初始化。
 *
 * 为什么复用主控台的语言包而不是自带一份：博物馆是**应用级验证台**，展示的就是主控台的业务视图，
 * 而这些视图的文案面很宽（几十个域、上千个 key）。包内的 `packages/ui/src/test/i18n.tsx` 自建最小资源
 * 时明确写过原则——「若某组件需要的 key 明显变多，正确做法是把文案改为 props 注入，而不是往资源里堆」，
 * 那条原则针对的是**包**（包不得依赖应用）。博物馆是应用，本就该模拟「应用侧提供 i18n 上下文」的真实场景，
 * 故直接取用主控台的中文包。
 *
 * 用 `?raw` 而非 JSON 模块导入：本仓 tsconfig 未开 `resolveJsonModule`，而 `?raw` 由 vite 提供、
 * 类型已在 `vite/client` 里声明，无需改动共享 tsconfig。语言包是纯数据、无初始化副作用
 * （应用侧的 `@/i18n` 会写 localStorage 与 document.lang，博物馆不引它）。
 */
import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import zhRaw from '../../control-plane-web/src/i18n/zh.json?raw'

const zh = JSON.parse(zhRaw) as Record<string, unknown>

void i18n.use(initReactI18next).init({
  resources: { zh: { translation: zh } },
  lng: 'zh',
  fallbackLng: 'zh',
  interpolation: { escapeValue: false },
})

export default i18n
