import React from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { App } from './App'
import { ToastHost, ErrorBoundary } from './components/ui'

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    {/* App-level boundary: a render crash anywhere shows a recovery card
        instead of a white screen — the cashier can always get back. */}
    <ErrorBoundary label="app">
      <App />
      <ToastHost />
    </ErrorBoundary>
  </React.StrictMode>,
)
