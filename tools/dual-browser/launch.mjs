// launch.mjs: tools/dual-browser.sh の --themes オプション用ランチャー。
//
// JSON仕様ファイル(引数1つ、ファイルパス)を読み込み、Playwrightの
// launchPersistentContext で複数ウィンドウ(colorScheme指定済み)を起動する。
// 起動後はプロセスを終了させず、SIGTERM/SIGINTを受けたら全ウィンドウを
// closeしてから終了する(呼び出し元のシェルはこのプロセスをバックグラウンドに
// 逃がして即座に制御を返す設計のため)。
import { chromium } from 'playwright-core';
import { readFileSync } from 'node:fs';

const specPath = process.argv[2];
if (!specPath) {
  console.error('launch.mjs: spec file path required');
  process.exit(1);
}

let spec;
try {
  spec = JSON.parse(readFileSync(specPath, 'utf8'));
} catch (err) {
  console.error('launch.mjs: failed to read/parse spec file', err);
  process.exit(1);
}

const contexts = [];
let shuttingDown = false;

async function main() {
  for (const w of spec.windows) {
    const ctx = await chromium.launchPersistentContext(w.userDataDir, {
      executablePath: spec.chromePath,
      headless: false,
      colorScheme: w.colorScheme,
      viewport: { width: w.width, height: w.height },
      isMobile: !!w.isMobile,
      hasTouch: !!w.hasTouch,
      deviceScaleFactor: w.deviceScaleFactor || 1,
      args: [
        '--disable-gpu',
        '--no-first-run',
        '--no-default-browser-check',
        '--disable-sync',
        '--disable-features=Translate,MediaRouter',
        `--window-position=${w.x},${w.y}`,
        `--window-size=${w.width},${w.height}`,
      ],
    });
    contexts.push(ctx);
    const page = ctx.pages()[0] ?? (await ctx.newPage());
    await page.goto(w.url);
    // 手元の画面上で、既存の他のウィンドウより手前に表示されるようにする。
    await page.bringToFront();
  }
  console.error(`launch.mjs: ${contexts.length} window(s) launched`);
}

async function shutdown() {
  if (shuttingDown) return;
  shuttingDown = true;
  await Promise.all(contexts.map((c) => c.close().catch(() => {})));
  process.exit(0);
}
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);

main().catch((err) => {
  console.error('launch.mjs: fatal error', err);
  process.exit(1);
});

// 起動が終わってもプロセスは終了させない(ウィンドウを保持し続けるため)。
setInterval(() => {}, 1 << 30);
