import js from '@eslint/js';
import { defineConfig } from 'eslint/config';
import tseslint from 'typescript-eslint';

// Runtime globals for the plain JavaScript files. TypeScript files get theirs from tsc,
// and typescript-eslint turns no-undef off for them.
const readonly = (names) => Object.fromEntries(names.map((name) => [name, 'readonly']));
const common = [
  'AbortController',
  'Blob',
  'FormData',
  'URL',
  'atob',
  'clearInterval',
  'clearTimeout',
  'console',
  'crypto',
  'fetch',
  'setInterval',
  'setTimeout',
  'structuredClone',
];
const node = readonly([...common, 'Buffer', 'process']);
const browser = readonly([...common, 'Option', 'confirm', 'document', 'innerWidth', 'window']);

export default defineConfig(
  { ignores: ['dist/', 'node_modules/'] },
  js.configs.recommended,
  tseslint.configs.recommended,
  { files: ['src/**/*.mjs', 'eslint.config.js'], languageOptions: { globals: node } },
  { files: ['web/**/*.js'], languageOptions: { globals: browser } },
  // Browser acceptance scripts pass callbacks to page.evaluate(), so tests see both sets.
  { files: ['test/**/*.mjs'], languageOptions: { globals: { ...node, ...browser } } }
);
