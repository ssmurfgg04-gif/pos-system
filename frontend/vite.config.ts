import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Minimal ambient declaration (avoids a @types/node dependency just for
// one env read in the config).
declare const process: { env: Record<string, string | undefined> }

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // Subdirectory deploys (the Netlify download site serves the demo under
  // /demo/) build with VITE_BASE=/demo/. Default '/' serves from the root
  // (the Go binary's embedded build and vite dev).
  base: process.env.VITE_BASE ?? '/',
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:3000',
      '/ws': { target: 'ws://127.0.0.1:3000', ws: true },
    },
  },
  build: { outDir: 'dist', sourcemap: false },
})
