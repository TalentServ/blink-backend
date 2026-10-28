import * as esbuild from 'esbuild'
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const appDir = process.argv[2]
const appFile = process.argv[3]
const kind = process.argv[4]
const exportName = process.argv[5] || 'App'
const here = path.dirname(fileURLToPath(import.meta.url))
const nodeModules = path.join(here, 'node_modules')

const blackTheme = `
:root, html, body, #root { background:#0b0d10; color:#e7e9ee; color-scheme:dark; }
body { margin:0; }
.unmerged-banner { margin:0; padding:8px 12px; background:#1a1408; color:#f6d48a; border-bottom:1px solid #3a2e14; font:600 14px system-ui,sans-serif; }
.calculator-app, .calculator, .history, main, section, form, [class*="calculation"], [class*="history"], [class*="result"] { background:#12161c; color:#e7e9ee; border-color:#2c3442; box-shadow:none; }
label, .expression-label, .expression-help, .empty-history, .history-expression { color:#b7c0ce; }
input, button, .keypad-button { background:#1c2330; color:#e7e9ee; border-color:#3a4558; }
input:focus, button:focus-visible, .keypad-button:focus-visible { outline-color:#60a5fa; }
button:hover, .keypad-button:hover { background:#273143; }
.equals-button { background:#1d4ed8; color:#fff; border-color:#3b82f6; }
.result, .history-result, h1, h2 { color:#f8fafc; }
.error { color:#fca5a5; }
`

if (kind === 'html') {
  const html = await readFile(path.join(appDir, appFile), 'utf8')
  process.stdout.write(html)
  process.exit(0)
}

const dir = await mkdtemp(path.join(tmpdir(), 'blink-preview-'))
try {
  const entry = path.join(dir, 'entry.tsx')
  const outfile = path.join(dir, 'preview.js')
  const relative = path.relative(dir, path.join(appDir, appFile)).replaceAll('\\', '/')
  const importLine = kind === 'named'
    ? `import { ${exportName} as Screen } from '${relative}'`
    : `import Screen from '${relative}'`
  await writeFile(entry, `import { createRoot } from 'react-dom/client'\n${importLine}\nconst root = document.getElementById('root')\nif (root) createRoot(root).render(<Screen expression="" result={null} entries={[]} sessionEntries={[]} />)\n`)
  await esbuild.build({
    entryPoints: [entry],
    absWorkingDir: dir,
    outfile,
    bundle: true,
    format: 'iife',
    platform: 'browser',
    target: 'es2022',
    jsx: 'automatic',
    jsxImportSource: 'react',
    define: { 'process.env.NODE_ENV': '"development"' },
    nodePaths: [nodeModules],
    loader: { '.css': 'css', '.tsx': 'tsx', '.ts': 'ts', '.jsx': 'jsx', '.js': 'js' },
  })
  const js = await readFile(outfile, 'utf8')
  let css = ''
  try {
    css = await readFile(path.join(dir, 'preview.css'), 'utf8')
  } catch {
    css = ''
  }
  const safeJs = js.replaceAll('</script', '<\\/script')
  process.stdout.write(`<!doctype html>
<html>
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<style>
${css}
${blackTheme}
</style>
</head>
<body>
<p class="unmerged-banner">Unmerged pull request. Nothing is merged.</p>
<div id="root"></div>
<script>${safeJs}</script>
</body>
</html>`)
} finally {
  await rm(dir, { recursive: true, force: true })
}
