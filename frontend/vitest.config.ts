import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    include: ['__tests__/**/*.test.{ts,tsx}'],
    // Shop-floor boxes are slow: the 5s default flakes under load while
    // the demo backend's latency windows (90–260ms × dozens of calls) add
    // up. 15s keeps failures honest (real hangs still fail, just later).
    testTimeout: 15000,
  },
})
