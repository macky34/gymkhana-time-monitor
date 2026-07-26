// phase-a-launch.mjs: tools/dual-browser/phase-a.sh 用のランチャー。
//
// 1つの headful launchPersistentContext を起動し、指定URLを開いたあと
// Chrome DevTools Protocol (CDP) のリモートデバッグポートを開いたまま待機し続ける。
// 起動後はプロセスを終了させず、SIGTERM/SIGINTを受けたらcloseしてから終了する
// (呼び出し元のシェルはこのプロセスをバックグラウンドに逃がして即座に制御を
// 返す設計のため。tools/dual-browser/launch.mjs と同じパターン)。
//
// phase-a.sh はこのスクリプトを PC×light / SP×light / PC×dark / SP×dark の
// 4窓分、計4回起動する(それぞれ別の --port・--profile-dir・--x/--y・
// --color-scheme、SP側のみ --mobile も渡す)。起動中、
// tools/dual-browser/phase-a-cmd.mjs が chromium.connectOverCDP() で
// このプロセスの --port に一時的に接続し、1操作ずつ実行してはすぐに切断する。
import { chromium } from 'playwright-core';

function parseArgs(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i += 1) {
    const key = argv[i];
    if (!key || !key.startsWith('--')) {
      continue;
    }
    if (key === '--mobile') {
      args.mobile = true;
      continue;
    }
    const value = argv[i + 1];
    args[key.slice(2)] = value;
    i += 1;
  }
  return args;
}

const args = parseArgs(process.argv.slice(2));
const chromePath = args.chrome;
const url = args.url;
const width = parseInt(args.width, 10);
const height = parseInt(args.height, 10);
const port = parseInt(args.port, 10);
const profileDir = args['profile-dir'];
const x = parseInt(args.x ?? '0', 10);
const y = parseInt(args.y ?? '0', 10);
const mobile = !!args.mobile;
const colorScheme = args['color-scheme'];

if (!chromePath || !url || !width || !height || !port || !profileDir) {
  console.error(
    'phase-a-launch.mjs: usage: --chrome <path> --url <url> --width <n> --height <n> --port <n> --profile-dir <dir> [--x <n>] [--y <n>] [--mobile] [--color-scheme light|dark]'
  );
  process.exit(1);
}

let ctx;
let shuttingDown = false;

async function main() {
  ctx = await chromium.launchPersistentContext(profileDir, {
    executablePath: chromePath,
    headless: false,
    viewport: { width, height },
    // isMobile: true を付けると viewport meta タグの無いページで
    // window.innerWidth が980前後になる(要求した幅と一致しない)ことを確認したが、
    // これはバグではなく実機のモバイルブラウザ(iOS Safari/Android Chrome)が
    // 非レスポンシブページに対して使う「仮想ビューポート(980px)」への正しい
    // フォールバック動作。このアプリの全ページは _shared.html 経由で
    // `<meta name="viewport" content="width=device-width, ...">` を持つため、
    // 実アプリを対象にする限り isMobile: true でも innerWidth は要求どおりになる
    // (実機検証で確認済み。tools/dual-browser/launch.mjs のSP窓と同じ設定)。
    ...(mobile ? { isMobile: true, hasTouch: true, deviceScaleFactor: 2 } : {}),
    ...(colorScheme ? { colorScheme } : {}),
    args: [
      '--disable-gpu',
      '--no-first-run',
      '--no-default-browser-check',
      '--disable-sync',
      '--disable-features=Translate,MediaRouter',
      `--window-position=${x},${y}`,
      `--remote-debugging-port=${port}`,
    ],
  });
  const page = ctx.pages()[0] ?? (await ctx.newPage());
  await page.goto(url);
  // 手元の画面上で、既存の他のウィンドウより手前に表示されるようにする。
  await page.bringToFront();
  console.error(`phase-a-launch.mjs: listening on CDP port ${port}`);
}

async function shutdown() {
  if (shuttingDown) return;
  shuttingDown = true;
  if (ctx) {
    await ctx.close().catch(() => {});
  }
  process.exit(0);
}
process.on('SIGTERM', shutdown);
process.on('SIGINT', shutdown);

main().catch((err) => {
  console.error('phase-a-launch.mjs: fatal error', err);
  process.exit(1);
});

// 起動が終わってもプロセスは終了させない(ウィンドウ・CDPポートを保持し続けるため)。
setInterval(() => {}, 1 << 30);
