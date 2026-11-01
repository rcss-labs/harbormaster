// Renders the PNG brand assets from the SVG/PNG sources with playwright's bundled Chromium.
// Usage: node web/scripts/render-icons.mjs   (or: make icons)
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { chromium } from '@playwright/test'

const here = path.dirname(fileURLToPath(import.meta.url))
const brandDir = path.resolve(here, '../public/brand')
const lockupSource = path.resolve(here, '../../docs/design/assets/main_logo.png')

const targets = [
  { source: path.join(brandDir, 'favicon.svg'), out: 'favicon-16.png', width: 16, height: 16 },
  { source: path.join(brandDir, 'favicon.svg'), out: 'favicon-32.png', width: 32, height: 32 },
  { source: path.join(brandDir, 'harbormaster-symbol.svg'), out: 'apple-touch-icon.png', width: 180, height: 180, background: '#FFFFFF', padding: 0.1 },
  { source: lockupSource, out: 'harbormaster-lockup.png', width: 1086, height: 362 },
]

const scratch = mkdtempSync(path.join(tmpdir(), 'hm-icons-'))
const browser = await chromium.launch()
try {
  for (const { source, out, width, height, background = 'transparent', padding = 0 } of targets) {
    // An <img> in a zero-margin page of the exact size gives a pixel-exact, transparent screenshot.
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: 1 })
    // Opaque targets (apple-touch-icon) get a solid background and padding: iOS composites transparency on black.
    const inset = Math.round(width * padding)
    const insetY = Math.round(height * padding)
    const html = path.join(scratch, `${out}.html`)
    writeFileSync(html, `<!doctype html><style>html,body{margin:0;background:${background}}img{display:block;position:absolute;left:${inset}px;top:${insetY}px}</style><img src="${pathToFileURL(source)}" width="${width - 2 * inset}" height="${height - 2 * insetY}">`)
    await page.goto(pathToFileURL(html).href)
    await page.screenshot({ path: path.join(brandDir, out), omitBackground: background === 'transparent' })
    await page.close()
    console.log(`wrote ${out} (${width}x${height})`)
  }
} catch (error) {
  console.error('render-icons failed:', error)
  process.exitCode = 1
} finally {
  await browser.close()
  rmSync(scratch, { recursive: true, force: true })
}
