import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { readFileSync } from 'fs'
import { resolve } from 'path'

// Read version from package.json
const pkg = JSON.parse(readFileSync(resolve(__dirname, 'package.json'), 'utf-8'))
const version = pkg.version || '0.0.0'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': resolve(__dirname, './src')
    }
  },
  define: {
    // Expose UI version as a global constant
    __UI_VERSION__: JSON.stringify(version)
  },
  server: {
    proxy: {
      '/api': 'http://localhost:9393'
    }
  },
  build: {
    // Output to dist/{version}/ - this ensures Go re-embeds when version changes
    outDir: `dist/${version}`,
    emptyOutDir: true,
    // The remaining large chunks are lazy-loaded third-party libraries
    // (elkjs, mermaid, cytoscape) that cannot be split further.
    chunkSizeWarningLimit: 1500
  }
})
