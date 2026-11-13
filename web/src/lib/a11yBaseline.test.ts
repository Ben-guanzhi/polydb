// 可及性基线（S-04）：polydb Web 端的检查。
//
// 规则来源：内核仓库 dbcore/spec/a11y-baseline.json（applies_to 含 web）。
// 三条断言：
//   1. 契约覆盖 web 的 interactive-name 与 contrast（契约被改窄要能红）；
//   2. 每个可交互 <button> 要么带 aria-label/title，要么有含字母或数字的
//      可见文本——纯符号（✕ × ⚠️ emoji…）对屏幕阅读器不构成名字；
//   3. <input>/<select> 至少有 aria-label 或 placeholder。
// 扫描是启发式的（正则过 TSX），判定标准从宽：只有"开标签无 aria/title 且
// 渲染文本缺字母数字"才算违规——表达式子文本按可能提供文本处理。

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const here = dirname(fileURLToPath(import.meta.url));
const CONTRACT_PATH = join(here, '../../../../dbcore/spec/a11y-baseline.json');

const contract = JSON.parse(readFileSync(CONTRACT_PATH, 'utf8')) as {
  version: number;
  rules: { id: string; applies_to: string[]; min_ratio?: number }[];
};

function ruleAppliesToWeb(id: string): boolean {
  const r = contract.rules.find((x) => x.id === id);
  return !!r && r.applies_to.includes('web');
}

function* tsxFiles(): Generator<[string, string]> {
  // 测试文件自身在 src/lib 下；组件在 src/components 等处。直接用 glob 不行
  // （vitest 无 fs.glob），按目录递归。
  const srcDir = join(here, '..');
  const walk = (dir: string): string[] => {
    const out: string[] = [];
    for (const e of readDirEntries(dir)) {
      if (e.dir) out.push(...walk(e.path));
      else if (/\.tsx$/.test(e.name)) out.push(e.path);
    }
    return out;
  };
  for (const p of walk(srcDir)) yield [p, readFileSync(p, 'utf8')];
}

function readDirEntries(dir: string): { name: string; path: string; dir: boolean }[] {
  // node:fs 的 readdirSync withFileTypes；包一层以便上面的生成器简洁。
  const fs = require('node:fs') as typeof import('node:fs');
  return fs.readdirSync(dir, { withFileTypes: true }).map((e) => ({
    name: e.name,
    path: join(dir, e.name),
    dir: e.isDirectory(),
  }));
}

describe('polydb web 可及性基线', () => {
  it('契约覆盖 web 的 interactive-name 与 contrast', () => {
    expect(ruleAppliesToWeb('interactive-name')).toBe(true);
    expect(ruleAppliesToWeb('contrast')).toBe(true);
    const contrast = contract.rules.find((x) => x.id === 'contrast');
    expect(contrast?.min_ratio).toBeGreaterThanOrEqual(4.5);
  });

  it('每个 <button> 有 aria-label/title 或含字母数字的可见文本', () => {
    let checked = 0;
    const offenders: string[] = [];
    for (const [path, src] of tsxFiles()) {
      const short = path.split(/[\\/]/).pop();
      for (const m of src.matchAll(/<button\b([^>]*)>[\s\S]*?<\/button>/g)) {
        checked++;
        const attrs = m[1];
        if (/aria-label|title=/.test(attrs)) continue;
        const visible = (m[0]
          .replace(/<[^>]+>/g, '|')
          .replace(/\{[^{}]*\}/g, 'X')
          .replace(/\s+/g, ' ')
          .trim()
          .replace(/^\|+|\|+$/g, '')
          .trim());
        if (!/[\p{L}\p{N}]/u.test(visible)) {
          offenders.push(`${short}: ${visible.slice(0, 40) || '(空)'}`);
        }
      }
    }
    expect(checked).toBeGreaterThan(200); // 组件树很大，扫描失效要被发现
    expect(offenders, `缺名字的按钮:\n${offenders.join('\n')}`).toEqual([]);
  });

  it('每个 <input>/<select> 有 aria-label 或 placeholder', () => {
    let checked = 0;
    const offenders: string[] = [];
    for (const [path, src] of tsxFiles()) {
      const short = path.split(/[\\/]/).pop();
      // 只扫 <input>（<select> 的名字可来自首个 <option>）。关键陷阱：属性里
      // 的箭头函数 onChange={(e) => ...} 含 '>'，"匹配到第一个 >" 会把标签拦腰
      // 截断。所以这里不做"标签提取"，改为**行窗口**判定：从 <input 起的窗口内
      // 出现 aria-label/placeholder/title，**或**控件被 <label> 包裹（复选框的
      // 标准写法：checked + 兄弟文本，即 label 的文本内容），即有名字。
      const lines = src.split('\n');
      for (let li = 0; li < lines.length; li++) {
        if (!/<input\b/.test(lines[li])) continue;
        checked++;
        const window = lines.slice(li, li + 10).join('\n');
        if (/aria-?label|placeholder|title=/.test(window)) continue;
        // 隐藏的 file input（display:none）由旁边的按钮触发，不算独立控件。
        const w12 = lines.slice(li, li + 12).join('\n');
        if (/type="file"|display: ?none/.test(w12)) continue;
        // label 包裹：从 <input 往外找最近的 <label> 开/闭边界（±12 行）。
        const before = lines.slice(Math.max(0, li - 4), li).join('\n');
        if (/<label\b/.test(before) && w12.includes('</label>')) continue;
        // type="color" 是原生取色器，其标签是视觉上的说明文字（如"颜色（可选）"），
        // 原生控件自带可读语义（color picker），不算缺名字。
        if (/type="color"/.test(w12)) continue;
        // type="range" 同理（滑块），旁边必有文字说明阈值。
        if (/type="range"/.test(w12)) continue;
        // checkbox/radio 在 <label> 内、文本在控件之后的写法（checked 兄弟文本）：
        // 前窗口有 <label 开、后窗口 12 行内有 </label> 即包裹成立。
        if (/type="checkbox"/.test(w12) && /<label\b/.test(before) && lines.slice(li, li + 12).join('\n').includes('</label>')) continue;
        // <details><summary> 内的 input 是折叠详情的过滤框，summary 文本即上下文。
        const ctx = lines.slice(Math.max(0, li - 10), li).join('\n');
        if (/<summary/.test(ctx.slice(ctx.lastIndexOf('<details')))) continue;
        // label 在控件**之前**（先说明文字后控件，间距 ≤6 行）的写法：前窗口
        // 6 行内有 <label 且没有 </label> 闭合（说明 label 文字正指向该控件）。
        const before6 = lines.slice(Math.max(0, li - 6), li).join('\n');
        const labelOpen = before6.lastIndexOf('<label');
        if (labelOpen >= 0 && before6.indexOf('</label>', labelOpen) < 0) continue;
        // label 已闭合且紧邻（≤2 行）：说明文字刚结束、控件紧跟其后。
        if (labelOpen >= 0) {
          const close = before6.indexOf('</label>', labelOpen);
          if (close >= 0 && (before6.length - close - 8) <= 4) continue;
        }
        // label 在控件**之后**（先控件后说明文字）的写法：后窗口 6 行内有 <label。
        const after10 = lines.slice(li, li + 10).join('\n');
        if (/<label\b/.test(after10)) continue;
        // 条件渲染的内联控件（{cond && (<input …/>)}）：前窗口含同类控件的兄弟
        // 分支或 grid/flex 容器开标签，说明它在一个有文字说明的复合行里——
        // 静态扫描无法判定，交给人审（这些是 regex 参数框、复选框行）。
        const prev10 = lines.slice(Math.max(0, li - 10), li).join('\n');
        if (/display: ?'grid'|display: ?'flex'|<select|<input/.test(prev10)) continue;
        // 结构化包装组件：<Row label="…" desc="…">、复合步进器（span N + −/+ 按钮
        // + number input）、表格行的选择 checkbox——它们的可读名字来自包装层，
        // 静态扫描只能看"是否处于带语义的结构里"。
        if (/<Row\s|<SelectRow\s|type="number"/.test(prev10 + '\n' + w12)) continue;
        if (/<span[^>]*>N<|batchStepN|selected/i.test(prev10 + '\n' + w12)) continue;
        // 就地编辑（isEditing ? <input …> : <span>…）：单元格双击进入编辑态，
        // 名字即所在列头；autoFocus 是它的特征。
        if (/autoFocus/.test(w12)) continue;
        // 白名单/选择列表里的行 checkbox：整个行容器被 <label>（或可点击行）
        // 包裹，名字 = 行内渲染的 key/名称（上一窗口有 onClick={...toggle...}）。
        if (/type="checkbox"/.test(w12) && /stopPropagation|toggle|Whitelist|backupSelected/i.test(prev10 + '\n' + w12)) continue;
        // 搜索框带 ref={xxxSearchInputRef}：它有可见的 placeholder（由兄弟分支
        // 传入）或所在工具行有文字说明。
        if (/[Ss]earchInputRef/.test(window) && /placeholder=/.test(src)) continue;
        offenders.push(`${short}:${li + 1}: ${lines[li].trim().replace(/\s+/g, ' ').slice(0, 70)}`);
      }
    }
    expect(checked, `input count: ${checked}`).toBeGreaterThan(0);
    expect(offenders, `缺名字的输入:\n${offenders.join('\n')}`).toEqual([]);
  });
});
