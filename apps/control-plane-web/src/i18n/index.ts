import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import zh from './zh.json'
import en from './en.json'

/** 同步 `<html lang>`，便于无障碍/浏览器识别页面语言（FR-132）。非 DOM 环境（单测）跳过。 */
function applyHtmlLang(lng: string) {
  if (typeof document !== 'undefined') {
    document.documentElement.lang = lng === 'en' ? 'en' : 'zh'
  }
}

const savedLang = localStorage.getItem('language') || 'zh'
applyHtmlLang(savedLang)

i18n.use(initReactI18next).init({
  resources: {
    zh: { translation: zh },
    en: { translation: en },
  },
  lng: savedLang,
  fallbackLng: 'zh',
  interpolation: {
    escapeValue: false,
  },
})

export function changeLanguage(lng: 'zh' | 'en') {
  localStorage.setItem('language', lng)
  applyHtmlLang(lng)
  void i18n.changeLanguage(lng)
}

export default i18n
