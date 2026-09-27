import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { enable } from '@carbon/feature-flags'
import App from './App'
import '@carbon/react/css/styles.css'
import './styles.css'

// Enable Carbon v12 features
enable('enable-v12-dynamic-floating-styles')
enable('enable-v12-release')

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
