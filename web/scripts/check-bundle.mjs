import { brotliCompressSync, gzipSync } from 'node:zlib'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

const assets = '../pkg/server/dist/assets'
const files = readdirSync(assets).filter(name => /\.(js|css)$/.test(name))
const limits = {
  'entry gzip': 130 * 1024,
  // WebGL (25 Kio gzip) est chargé à la demande sur desktop ; Unicode 11,
  // le Mur et À traiter augmentent aussi le budget global. Le panneau Sudo et
  // la touche du micro ajoutent environ 4 Kio au chunk Terminal. Marge d’environ 3 %.
  'xterm gzip': 102 * 1024,
  // L’explorateur de fichiers (éditeur, Markdown, coloration) pèse environ
  // 42 Kio brotli, la vue Docker (conteneurs, images, volumes) environ 14,
  // l’onglet Boucles et les comptes CLI du monitoring environ 20. Tous sont
  // chargés à la demande. Marge d’environ 3 %.
  'total brotli': 340 * 1024,
}
let entryGzip = 0
let xtermGzip = 0
let totalBrotli = 0
for (const name of files) {
  const raw = readFileSync(join(assets, name))
  const gzip = gzipSync(raw, { level: 9 }).length
  totalBrotli += brotliCompressSync(raw).length
  if (name.startsWith('index-')) entryGzip += gzip
  if (name.startsWith('xterm-') || name.startsWith('Terminal-')) xtermGzip += gzip
}
const measurements = {
  'entry gzip': entryGzip,
  'xterm gzip': xtermGzip,
  'total brotli': totalBrotli,
}
for (const [name, size] of Object.entries(measurements)) {
  console.log(`${name}: ${size} / ${limits[name]} bytes`)
  if (size > limits[name]) process.exitCode = 1
}
