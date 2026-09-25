#!/usr/bin/env node
// 用 Edge 的 DevTools Protocol 量「真正的」hover／focus 樣式。
//
// 為什麼需要這支：tool.css 的通用規則、各頁規則與按鈕變體之間，是靠權重與先後順序決定
// 誰贏。這種「平常看沒問題、滑過去才發現被蓋掉」的錯（選取中的分頁／已按下的分段按鈕
// 被 hover 規則塗掉），看程式碼或算權重都不保險——這支會送真的滑鼠與鍵盤事件，
// 再讀 computed style，用實際的顏色值判斷。
//
//   node hover-check.mjs            # 需要 Node 22+（用全域 WebSocket）與 Edge，不用先啟動服務
//   EDGE=<msedge.exe 路徑> node hover-check.mjs
//   node --no-experimental-websocket hover-check.mjs   # 驗版本守衛：應該印訊息、結束碼 2，不留東西
//
// 它自己起一個只服務 web/ 的靜態伺服器（隨機 port）與 headless Edge，跑完會收乾淨。
// 顏色是從頁面上的 CSS 變數讀出來比對的，所以淺色／深色模式都適用。
// 有 ✗ 時結束碼為 1，可以掛進 CI 或 pre-commit。
import { createServer } from 'node:http';
import { accessSync, mkdtempSync, rmSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { extname, join, normalize, sep } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

const WEB = fileURLToPath(new URL('./web/', import.meta.url));
const MIME = {
  '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8',
  '.json': 'application/json', '.png': 'image/png', '.ico': 'image/vnd.microsoft.icon',
  '.svg': 'image/svg+xml', '.webmanifest': 'application/manifest+json',
};
const sleep = ms => new Promise(r => setTimeout(r, ms));

// 這支用全域 WebSocket（Node 22 起才預設有）；沒有時講清楚，不要丟 "WebSocket is not defined"。
// 不能只看版本號：舊版 Node 與「Node 22+ 但關掉 --experimental-websocket」都會缺，訊息兩種都要說得通。
if (typeof WebSocket === 'undefined') {
  console.error(`這支需要全域 WebSocket（Node 22+ 預設就有）；目前是 ${process.version}，`
    + '但全域 WebSocket 不存在（舊版 Node，或啟動時加了 --no-experimental-websocket）');
  process.exit(2);
}

function edgePath() {
  const cands = [
    process.env.EDGE,
    join(process.env['ProgramFiles(x86)'] || '', 'Microsoft', 'Edge', 'Application', 'msedge.exe'),
    join(process.env.ProgramFiles || '', 'Microsoft', 'Edge', 'Application', 'msedge.exe'),
    join(process.env.LOCALAPPDATA || '', 'Microsoft', 'Edge', 'Application', 'msedge.exe'),
    '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge',
    '/usr/bin/microsoft-edge',
    '/usr/bin/microsoft-edge-stable',
  ].filter(Boolean);
  for (const c of cands) {
    try { accessSync(c); return c; } catch { /* 換下一個 */ }
  }
  console.error('找不到 Edge；請用 EDGE=<msedge 執行檔路徑> 指定');
  process.exit(2);
}

// ---- 只服務 web/ 的靜態伺服器 ----
const server = createServer(async (req, res) => {
  let p = decodeURIComponent(new URL(req.url, 'http://x').pathname);
  if (p.endsWith('/')) p += 'index.html';
  const file = normalize(join(WEB, p));
  if (!file.startsWith(WEB.endsWith(sep) ? WEB : WEB + sep)) { res.writeHead(403); res.end(); return; }
  try {
    const body = await readFile(file);
    res.writeHead(200, { 'Content-Type': MIME[extname(file).toLowerCase()] || 'application/octet-stream' });
    res.end(body);
  } catch {
    res.writeHead(404); res.end('not found');
  }
});
await new Promise(r => server.listen(0, '127.0.0.1', r));
const origin = `http://127.0.0.1:${server.address().port}`;

// ---- headless Edge + remote debugging ----
const debugPort = Number(process.env.DEBUG_PORT) || 9400 + Math.floor(Math.random() * 400);
const profile = mkdtempSync(join(tmpdir(), 'pdf-toolbox-hover-'));
const child = spawn(edgePath(), [
  '--headless=new', '--disable-gpu', '--no-first-run', '--window-size=1000,1000',
  `--user-data-dir=${profile}`, `--remote-debugging-port=${debugPort}`, `${origin}/protect/`,
], { stdio: 'ignore' });

// Edge 要等它真的結束才刪得掉 profile（還開著的檔案會讓 rmSync 失敗），最多等 5 秒
async function cleanup(code) {
  const exited = child.exitCode !== null || child.signalCode !== null
    ? Promise.resolve()
    : new Promise(r => child.once('exit', r));
  try { child.kill(); } catch {}
  await Promise.race([exited, sleep(5000)]);
  try { server.close(); } catch {}
  try {
    rmSync(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  } catch (e) {
    console.error(`暫存 profile 沒刪掉，請手動刪除：${profile}（${e.message}）`);
  }
  process.exit(code);
}

// 主體包在 main 裡：中途丟例外也一定會走 cleanup，不會留下 headless Edge
async function main() {
  let target = null;
  for (let i = 0; i < 60 && !target; i++) {
    await sleep(250);
    try {
      const list = await (await fetch(`http://127.0.0.1:${debugPort}/json/list`)).json();
      target = list.find(t => t.type === 'page');
    } catch { /* 還沒起來 */ }
  }
  if (!target) { console.error('Edge 的 debugging port 沒起來'); return 2; }

  const ws = new WebSocket(target.webSocketDebuggerUrl);
  let seq = 0;
  const pending = new Map();
  ws.onmessage = e => {
    const m = JSON.parse(e.data);
    if (m.id && pending.has(m.id)) { pending.get(m.id)(m.result); pending.delete(m.id); }
  };
  await new Promise(r => ws.addEventListener('open', r));
  const send = (method, params = {}) => new Promise(res => {
    const id = ++seq;
    pending.set(id, res);
    ws.send(JSON.stringify({ id, method, params }));
  });
  await send('Page.enable');

  const evaluate = async expression => {
    const r = await send('Runtime.evaluate', { expression, returnByValue: true });
    return r.result && r.result.value;
  };
  const park = async () => {
    await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: 2, y: 2, button: 'none' });
    await sleep(150);
  };
  const colourOf = selector => evaluate(`getComputedStyle(document.querySelector(${JSON.stringify(selector)})).backgroundColor`);

  async function hoverColour(selector) {
    await park();
    const at = await evaluate(`(() => {
      const el = document.querySelector(${JSON.stringify(selector)});
      if (!el) return null;
      el.scrollIntoView({ block: 'center' });
      const b = el.getBoundingClientRect();
      return { x: Math.round(b.x + b.width / 2), y: Math.round(b.y + b.height / 2) };
    })()`);
    if (!at) return '找不到元素';
    await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: at.x, y: at.y, button: 'none' });
    await sleep(250);
    return colourOf(selector);
  }

  const goto = async path => {
    await send('Page.navigate', { url: origin + path });
    await sleep(1000);
    // 設計語言的顏色一律從頁面上的變數讀，淺色／深色模式都適用
    return evaluate(`(() => {
      const s = getComputedStyle(document.documentElement);
      const v = n => s.getPropertyValue(n).trim();
      const px = hex => {
        const c = document.createElement('span');
        c.style.color = hex;
        document.body.append(c);
        const out = getComputedStyle(c).color;
        c.remove();
        return out;
      };
      return { accent: px(v('--accent')), hover: px(v('--accent-hover')), line: px(v('--line')),
               soft: px(v('--soft')), warn: px(v('--warn')) };
    })()`);
  };

  let bad = 0;
  const check = (name, got, want) => {
    const ok = got === want;
    if (!ok) bad++;
    console.log(`  ${ok ? '✓' : '✗'} ${name}：${got}${ok ? '' : `  ← 想要 ${want}`}`);
  };
  const head = t => console.log(`\n${t}`);

  // ---- 1. 首頁外框的分頁 ----
  let C = await goto('/');
  head('首頁外框（分頁）');
  check('未選取 平常是透明', await colourOf('nav button:not([aria-selected=true])'), 'rgba(0, 0, 0, 0)');
  check('未選取 滑過用 --line', await hoverColour('nav button:not([aria-selected=true])'), C.line);
  check('已選取 平常是 --accent', await colourOf('nav button[aria-selected=true]'), C.accent);
  check('已選取 滑過要維持 --accent', await hoverColour('nav button[aria-selected=true]'), C.accent);

  // ---- 2. 密碼頁的分段控制與按鈕變體 ----
  C = await goto('/protect/');
  head('密碼頁（分段控制）');
  check('未選取 平常是 --soft', await colourOf('.seg button:not([aria-pressed=true])'), C.soft);
  check('未選取 滑過用 --line', await hoverColour('.seg button:not([aria-pressed=true])'), C.line);
  check('已按下 平常是 --accent', await colourOf('.seg button[aria-pressed=true]'), C.accent);
  check('已按下 滑過要維持 --accent', await hoverColour('.seg button[aria-pressed=true]'), C.accent);

  // 按鈕變體：臨時插一組「樣本」進來量，跟哪一頁用到無關
  await evaluate(`(() => {
    const box = document.createElement('div');
    box.style.cssText = 'position:fixed;left:0;top:0;z-index:99999';
    box.innerHTML = '<button id="__primary">主要</button>'
      + '<button id="__danger" class="danger">危險</button><button id="__off" disabled>停用</button>';
    document.body.append(box);
  })()`);
  head('按鈕變體（暫時插入的樣本）');
  check('主要按鈕 平常是 --accent', await colourOf('#__primary'), C.accent);
  check('主要按鈕 滑過用 --accent-hover', await hoverColour('#__primary'), C.hover);
  check('危險（danger）平常是透明', await colourOf('#__danger'), 'rgba(0, 0, 0, 0)');
  check('危險（danger）滑過用 --warn', await hoverColour('#__danger'), C.warn);
  check('停用的按鈕 滑過不變', await hoverColour('#__off'), C.accent);

  // ---- 3. 鍵盤 focus 看得到外框 ----
  head('鍵盤 focus（:focus-visible）');
  await park();
  for (let i = 0; i < 3; i++) {
    await send('Input.dispatchKeyEvent', { type: 'rawKeyDown', windowsVirtualKeyCode: 9, code: 'Tab', key: 'Tab' });
    await send('Input.dispatchKeyEvent', { type: 'keyUp', windowsVirtualKeyCode: 9, code: 'Tab', key: 'Tab' });
    await sleep(120);
  }
  // Tab 之後 :focus-visible 不一定馬上生效（剛啟動的 Edge 尤其慢），等到它成立再量，最多 2 秒
  for (let i = 0; i < 20; i++) {
    if (await evaluate(`!!document.activeElement && document.activeElement.matches(':focus-visible')`)) break;
    await sleep(100);
  }
  const focus = await evaluate(`(() => {
    const el = document.activeElement;
    if (!el || el === document.body) return null;
    const s = getComputedStyle(el);
    return { focusVisible: el.matches(':focus-visible'), docFocus: document.hasFocus(), tag: el.tagName.toLowerCase() + (el.id ? '#' + el.id : ''), width: s.outlineWidth, style: s.outlineStyle, color: s.outlineColor };
  })()`);
  if (!focus) {
    check('Tab 之後要有元素取得焦點', '沒有', '元素');
  } else {
    console.log(`  · 焦點在 ${focus.tag}（:focus-visible=${focus.focusVisible}，document.hasFocus()=${focus.docFocus}）`);
    check('focus 外框寬度', focus.width, '2px');
    check('focus 外框樣式', focus.style, 'solid');
    check('focus 外框顏色用 --accent', focus.color, C.accent);
  }

  console.log(bad ? `\n有 ${bad} 項不符` : '\n全部符合');
  return bad ? 1 : 0;
}

let code = 2;
try {
  code = await main();
} catch (e) {
  console.error(e);
} finally {
  await cleanup(code);
}
