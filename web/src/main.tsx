import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import './fonts'
import App from './App'
import { ApplicationErrorBoundary } from './components/ApplicationErrorBoundary'
import { recoverModuleLoad } from './lib/moduleRecovery'

window.addEventListener('vite:preloadError', recoverModuleLoad)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ApplicationErrorBoundary>
      <App />
    </ApplicationErrorBoundary>
  </StrictMode>,
)
