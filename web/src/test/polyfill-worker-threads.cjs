/**
 * Preload for Vitest worker processes.
 *
 * jsdom@30 pulls undici@8, which calls worker_threads.markAsUncloneable when
 * constructing CacheStorage. That API exists on Node 22+ but not Node 20, so
 * `npm test` fails with:
 *   TypeError: webidl.util.markAsUncloneable is not a function
 *
 * CI uses Node 24; local/dev machines may still be on Node 20. This no-op
 * polyfill keeps the jsdom environment loadable on both.
 *
 * Must be loaded via node -r before any module imports undici/jsdom
 * (see vitest.config.ts poolOptions.forks.execArgv).
 */
'use strict'

const workerThreads = require('node:worker_threads')

if (typeof workerThreads.markAsUncloneable !== 'function') {
  Object.defineProperty(workerThreads, 'markAsUncloneable', {
    value: function markAsUncloneable() {
      /* no-op on Node versions without structured-clone unclonable tagging */
    },
    configurable: true,
    enumerable: false,
    writable: true,
  })
}
