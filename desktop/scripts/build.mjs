import { copyFile, mkdir, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';
import sharp from 'sharp';
import png2icons from 'png2icons';

const root = fileURLToPath(new URL('../', import.meta.url));
await mkdir(`${root}dist/agents`, { recursive: true });
for (const name of ['darwin_inspector.js', 'windows_bootstrap.js']) {
  await copyFile(
    `${root}../internal/host/${name}`,
    `${root}dist/agents/${name}`,
  );
}
await copyFile(
  `${root}src/bootstrapper/discover.ps1`,
  `${root}dist/bootstrapper/discover.ps1`,
);
await build({
  entryPoints: [`${root}src/main/preload.ts`],
  bundle: true,
  platform: 'node',
  format: 'cjs',
  external: ['electron'],
  outfile: `${root}dist/main/preload.cjs`,
});
const svg = await readFile(`${root}build/icon.svg`);
const png = await sharp(svg).png().toBuffer();
const icns = png2icons.createICNS(png, png2icons.BILINEAR, 0);
const ico = png2icons.createICO(png, png2icons.BILINEAR, 0, true);
if (!icns || !ico) throw new Error('ICON_GENERATION_FAILED');
await writeFile(`${root}build/icon.png`, png);
await writeFile(`${root}build/icon.icns`, icns);
await writeFile(`${root}build/icon.ico`, ico);
await sharp(svg).resize(32, 32).png().toFile(`${root}build/tray.png`);
