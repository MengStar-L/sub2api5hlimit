import { afterEach } from 'vitest'
import { config } from '@vue/test-utils'

Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: query.includes('prefers-reduced-motion'),
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  }),
})

globalThis.requestAnimationFrame = callback => window.setTimeout(() => callback(performance.now()), 0)
globalThis.cancelAnimationFrame = id => window.clearTimeout(id)
config.global.stubs = { transition: true, 'transition-group': true }

afterEach(() => document.body.replaceChildren())
