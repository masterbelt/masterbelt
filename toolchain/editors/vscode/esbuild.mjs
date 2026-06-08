import * as esbuild from 'esbuild';

const watch = process.argv.includes('--watch');
const production = process.argv.includes('--production');

/** @type {import('esbuild').BuildOptions} */
const options = {
  entryPoints: ['src/extension.ts'],
  bundle: true,
  outfile: 'dist/extension.js',
  // The `vscode` module is provided by the editor at runtime, never bundled.
  // Everything else (vscode-languageclient and its deps) is bundled into the
  // one file VS Code loads — the editor's recommended shape, smaller and faster
  // to load than shipping node_modules.
  external: ['vscode'],
  format: 'cjs',
  platform: 'node',
  target: 'node20',
  // Minify the published build to shrink the bundle; keep dev and watch builds
  // readable and source-mapped for debugging.
  minify: production,
  sourcemap: !production,
  logLevel: 'info',
};

if (watch) {
  const ctx = await esbuild.context(options);
  await ctx.watch();
} else {
  await esbuild.build(options);
}
