import { copyFileSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build, defineConfig } from 'vite';
import type { Plugin } from 'vite';

const root = dirname(fileURLToPath(import.meta.url));

function chromeExtensionAssets(): Plugin {
  return {
    name: 'chrome-extension-assets',
    async closeBundle() {
      const dist = resolve(root, 'dist');
      mkdirSync(dist, { recursive: true });
      copyFileSync(resolve(root, 'manifest.json'), resolve(dist, 'manifest.json'));

      const nestedHtml = resolve(dist, 'src/sidepanel/index.html');
      if (existsSync(nestedHtml)) {
        const html = readFileSync(nestedHtml, 'utf8')
          .replaceAll('../../assets/', './assets/')
          .replaceAll('../assets/', './assets/');
        writeFileSync(resolve(dist, 'sidepanel.html'), html);
      }

      const iconsSrc = resolve(root, 'public/icons');
      const iconsDest = resolve(dist, 'public/icons');
      if (existsSync(iconsSrc)) {
        mkdirSync(iconsDest, { recursive: true });
        for (const name of ['icon-16.png', 'icon-48.png', 'icon-128.png', 'icon.svg']) {
          const from = resolve(iconsSrc, name);
          if (existsSync(from)) {
            copyFileSync(from, resolve(iconsDest, name));
          }
        }
      }

      // Content scripts injected via chrome.scripting.executeScript cannot be ES modules.
      await build({
        configFile: false,
        root,
        logLevel: 'warn',
        build: {
          emptyOutDir: false,
          outDir: 'dist',
          lib: {
            entry: resolve(root, 'src/content/content-script.ts'),
            name: 'astonishContent',
            formats: ['iife'],
            fileName: () => 'content-script.js',
          },
          rollupOptions: {
            output: {
              inlineDynamicImports: true,
            },
          },
        },
      });
    },
  };
}

export default defineConfig({
  base: './',
  plugins: [chromeExtensionAssets()],
  build: {
    modulePreload: false,
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      input: {
        sidepanel: resolve(root, 'src/sidepanel/index.html'),
        'service-worker': resolve(root, 'src/background/service-worker.ts'),
      },
      output: {
        entryFileNames: (chunk) =>
          chunk.name === 'service-worker' ? 'service-worker.js' : 'assets/[name]-[hash].js',
        chunkFileNames: 'assets/[name]-[hash].js',
        assetFileNames: 'assets/[name]-[hash][extname]',
      },
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
  },
});
