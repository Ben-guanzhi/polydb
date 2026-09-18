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
      // react-hooks v7 的编译器型新规则对历史代码过于激进，先关闭；
      // 保留稳定且高价值的 rules-of-hooks / exhaustive-deps（warn）
      'react-hooks/purity': 'off',
      'react-hooks/set-state-in-effect': 'off',
      'react-hooks/refs': 'off',
      'react-hooks/static-components': 'off',
      'react-hooks/immutability': 'off',
      'react-hooks/preserve-manual-memoization': 'off',
      '@typescript-eslint/no-unused-vars': ['warn', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-require-imports': 'off',
      // if-else 链 + 末尾统一消费的模式（如键盘 handler 里 let x=null → 分支赋值 → 末尾 if(x)）被误报，关掉
      'no-useless-assignment': 'off',
      'react-refresh/only-export-components': 'off',
    },
  },
);