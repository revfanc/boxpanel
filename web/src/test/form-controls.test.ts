import { expect, it } from 'vitest'

const sources = import.meta.glob<string>(['/src/**/*.tsx', '!/src/components/ui/**', '!/src/**/*.test.tsx'], {
  eager: true,
  query: '?raw',
  import: 'default',
})

it('uses shared UI components for select, radio, checkbox and textarea', () => {
  const nativeControl = /<(?:select|textarea)\b|<input\b[^>]*\btype\s*=\s*(?:["'](?:radio|checkbox)["']|\{\s*["'](?:radio|checkbox)["']\s*\})/
  expect(Object.keys(sources)).toContain('/src/components/CopyField.tsx')
  expect(Object.entries(sources).filter(([, source]) => nativeControl.test(source)).map(([path]) => path)).toEqual([])
})
