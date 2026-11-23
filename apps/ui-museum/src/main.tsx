import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
// 业务视图的文案要能显示出中文而非 i18n key，故在挂载前初始化 i18n（见该文件的说明）。
import './i18n'
import './styles.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
