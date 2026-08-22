import { defineConfig } from 'vite'
import { readFileSync } from 'fs'
import { resolve } from 'path'
import { fileURLToPath } from 'url'

const root = fileURLToPath(new URL('.', import.meta.url))
const pkg = JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf-8'))
const version = pkg.version || '0.0.0'

export default defineConfig({
  build: {
    outDir: `dist/${version}`,
    emptyOutDir: false,
    lib: {
      entry: resolve(root, 'src/components/docs/slides/runtime/index.ts'),
      name: 'AstonishSlidesRuntime',
      formats: ['iife'],
      fileName: () => 'slides-runtime.js',
    },
    rollupOptions: { output: { manualChunks: undefined } },
    chunkSizeWarningLimit: 1000,
  },
})
