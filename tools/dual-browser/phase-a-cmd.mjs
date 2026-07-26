// phase-a-cmd.mjs: tools/dual-browser/phase-a-launch.mjs が開いている
// headful ブラウザに Chrome DevTools Protocol (CDP) 経由で一時的に接続し、
// 指定した1操作を実行して結果をJSONで標準出力に出し、すぐに終了する。
//
// ブラウザ自体は閉じない(browser.close() は絶対に呼ばない)。Claude(呼び出し側)は
// /live-check Phase A の検証手順(ログイン・画面遷移・ボタン操作・
// PC/SP×light/darkスイープ等)の1ステップごとにこのスクリプトを1回ずつ実行する。
//
// セキュリティ上の注意: "eval" アクションは page.evaluate() に渡された文字列を
// そのままJSとして評価する(Playwrightのpage.evaluateは文字列を渡すと式として
// 評価する)。この仕組みはこのマシン上の同一ユーザー(Claude自身)が自分で
// 起動したブラウザに対してのみ使うローカル開発専用ツールであり、外部入力を
// 受け付けるものではない。既存の browser_run_code_unsafe MCPツールと同等の
// 位置づけ(ローカル開発専用、RCE相当)である。
//
// 実装上の注意: Chrome DevTools Protocol の Emulation.setEmulatedMedia は
// CDPセッション単位の状態であり、接続していたクライアント(このプロセス)が
// 切断すると、ブラウザ側で自動的にリセットされる(実機検証で確認済み。
// 一方 setViewportSize によるビューポートサイズの上書きは切断後も保持される)。
// このスクリプトは1操作ごとに新しいCDP接続を張っては切断する設計のため、
// 前回の emulateMedia 呼び出しで指定した colorScheme を毎回再適用しないと、
// 次のステップ(screenshot 等)では常にデフォルト(light)に戻ってしまう。
// これを避けるため、ポートごとに最後に指定した colorScheme を一時ファイルに
// 記録し、以後の全アクション実行前に再適用する。
import { chromium } from 'playwright-core';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const port = process.argv[2];
const actionArg = process.argv[3];

if (!port || !actionArg) {
  console.error("Usage: node tools/dual-browser/phase-a-cmd.mjs <port> '<JSON action>'");
  process.exit(1);
}

let action;
try {
  action = JSON.parse(actionArg);
} catch (err) {
  console.error('phase-a-cmd.mjs: failed to parse JSON action', err);
  process.exit(1);
}

const statePath = join(tmpdir(), `phase-a-cmd-state-${port}.json`);

function loadState() {
  if (!existsSync(statePath)) {
    return {};
  }
  try {
    return JSON.parse(readFileSync(statePath, 'utf8'));
  } catch {
    return {};
  }
}

function saveState(state) {
  try {
    writeFileSync(statePath, JSON.stringify(state));
  } catch {
    // 記録に失敗しても致命的ではない(次回以降 colorScheme の再適用が
    // 効かなくなるだけ)ので握りつぶす。
  }
}

const browser = await chromium.connectOverCDP(`http://127.0.0.1:${port}`);
try {
  const ctx = browser.contexts()[0];
  const page = ctx.pages()[0] ?? (await ctx.newPage());

  const state = loadState();
  if (state.colorScheme) {
    await page.emulateMedia({ colorScheme: state.colorScheme }).catch(() => {});
  }

  let result;
  switch (action.action) {
    case 'goto':
      await page.goto(action.url);
      result = { ok: true };
      break;
    case 'resize':
      await page.setViewportSize({ width: action.width, height: action.height });
      result = { ok: true };
      break;
    case 'emulateMedia':
      await page.emulateMedia({ colorScheme: action.colorScheme });
      state.colorScheme = action.colorScheme;
      saveState(state);
      result = { ok: true };
      break;
    case 'click':
      await page.click(action.selector);
      result = { ok: true };
      break;
    case 'fill':
      await page.fill(action.selector, action.value);
      result = { ok: true };
      break;
    case 'screenshot':
      await page.screenshot({ path: action.path });
      result = { ok: true, path: action.path };
      break;
    case 'eval':
      // eslint-disable-next-line no-eval -- ローカル開発専用ツール(冒頭コメント参照)
      result = { ok: true, value: await page.evaluate(action.expr) };
      break;
    default:
      result = { ok: false, error: `unknown action: ${action.action}` };
  }

  console.log(JSON.stringify(result));
} catch (err) {
  console.log(JSON.stringify({ ok: false, error: String(err && err.message ? err.message : err) }));
}

// browser.close() は絶対に呼ばない(実ブラウザが閉じてしまう)。プロセスは自然終了させる。
process.exit(0);
