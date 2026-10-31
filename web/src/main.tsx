import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { registerServiceWorker } from './pwa'
import { initLang } from './i18n'
import './app.css'

// 先定语言再渲染：翻译函数读模块级变量
initLang()
void registerServiceWorker()
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
