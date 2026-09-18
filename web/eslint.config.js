import js from '@eslint/js';
import tseslint from 'typescript-eslint';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';

const browserGlobals = [
  'window', 'document', 'navigator', 'console', 'URL', 'Blob', 'FileReader',
  'setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'localStorage',
  'performance', 'CustomEvent', 'crypto', 'TextDecoder', 'TextEncoder',
  'createObjectURL', 'revokeObjectURL', 'AddEventListenerOptions',
];

export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'src/api'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2022,
      globals: Object.fromEntries(browserGlobals.map((g) => [g, 'readonly'])),
    },
    plugins: { 'react-hooks': reactHooks, 'react-refresh': reactRefresh },
    rules: {
      ...reactHooks.configs.recommended.rules,
      '@typescript-eslint/no-unused-vars': ['warn', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-require-imports': 'off',
      'react-refresh/only-export-components': 'off',
    },
  },
);