import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    // v1.8.0: the src-side suites run too — lib goldens (money, qty),
    // the demo parity pins and the MoneyInput regression (it opts into
    // jsdom via its per-file docblock; so does the demo parity suite).
    include: ['__tests__/**/*.test.{ts,tsx}', 'src/**/*.test.{ts,tsx}'],
    // Shop-floor boxes are slow: the 5s default flakes under load while
    // the demo backend's latency windows (90–260ms × dozens of calls) add
    // up. 15s keeps failures honest (real hangs still fail, just later).
    testTimeout: 15000,
  },
})
