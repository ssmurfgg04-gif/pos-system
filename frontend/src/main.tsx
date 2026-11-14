import React from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { App } from './App'
import { ToastHost, ErrorBoundary } from './components/ui'
import { CustomerDisplay } from './pages/CustomerDisplay'
import { stripBase } from './lib/router'

// The customer-facing screen is a standalone surface: no login, no API, no
// app shell. It only renders what the till window broadcasts to it.
const isCustomerDisplay = stripBase(location.pathname) === '/display'

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    {/* App-level boundary: a render crash anywhere shows a recovery card
        instead of a white screen, the cashier can always get back. */}
    <ErrorBoundary label="app">
      {isCustomerDisplay ? <CustomerDisplay /> : (
        <>
          <App />
          <ToastHost />
        </>
      )}
    </ErrorBoundary>
  </React.StrictMode>,
)
