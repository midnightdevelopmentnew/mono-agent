import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    // Exclude macOS AppleDouble sidecar files (._*) this volume creates, which
    // are not valid JS and otherwise break the run.
    exclude: ['**/node_modules/**', '**/dist/**', '**/._*'],
    include: ['src/**/*.{test,spec}.{js,jsx}'],
  },
})
