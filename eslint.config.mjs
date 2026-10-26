import js from '@eslint/js'
import ts from 'typescript-eslint'
import vue from 'eslint-plugin-vue'
import vueParser from 'vue-eslint-parser'

export default ts.config(
  { ignores: ['**/dist/**', '**/out/**', '**/release/**', '**/node_modules/**', '**/.astro/**', 'apps/website/artifacts/**', 'packages/sdk/src/**', 'internal/platform/web/**'] },
  js.configs.recommended,
  ...ts.configs.recommended,
  ...vue.configs['flat/recommended'],
  {
    files: ['**/*.{ts,vue}'],
    languageOptions: {
      parserOptions: { parser: ts.parser },
      globals: { window: 'readonly', document: 'readonly', fetch: 'readonly', console: 'readonly', ResizeObserver: 'readonly', navigator: 'readonly', requestAnimationFrame: 'readonly', URL: 'readonly', File: 'readonly', Blob: 'readonly', EventSource: 'readonly', setTimeout: 'readonly', clearTimeout: 'readonly' },
    },
    rules: {
      'quotes': ['error', 'single', { avoidEscape: true }],
      'semi': ['error', 'never'],
      'vue/multi-word-component-names': 'off',
      // TypeScript resolves type names and browser globals in these files.
      'no-undef': 'off',
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_' }],
    },
  },
  { files: ['**/*.vue'], languageOptions: { parser: vueParser, parserOptions: { parser: ts.parser, extraFileExtensions: ['.vue'] } } },
  { files: ['scripts/**/*.mjs', 'apps/website/scripts/**/*.mjs', '**/*.config.mjs'], languageOptions: { globals: { process: 'readonly', console: 'readonly', URL: 'readonly', Buffer: 'readonly', fetch: 'readonly', AbortSignal: 'readonly', setTimeout: 'readonly', clearTimeout: 'readonly' } } },
)
