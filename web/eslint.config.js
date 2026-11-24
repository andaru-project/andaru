import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  { ignores: ['dist', 'dist-netplane'] },
  ...tseslint.configs.recommended,
  reactHooks.configs.flat.recommended,
	{
		files: ['src/**/*.{ts,tsx}'],
		ignores: ['src/test/**'],
		plugins: { 'react-refresh': reactRefresh },
		rules: {
			'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
		},
	},
);
