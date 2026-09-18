// SQL 语句切分器：按分号分句，忽略字符串和注释内的分号。
//
// 支持：
//   - 单引号字符串 '...'（内部 ' 转义成 ''）
//   - 双引号字符串 "..."
//   - 反引号标识符 `...`（MySQL）
//   - 方括号标识符 [...]（SQL Server）
//   - 单行注释 -- ...（到行尾）
//   - 单行注释 # ...（到行尾，MySQL）
//   - 多行注释 /* ... */（不支持嵌套，与主流 DB 一致）
//
// 注释本身不构成 statement；纯注释/空白的段会被丢弃。
//
// 返回：每个 statement 的 { sql, start, end }（start/end 是原文下标，end 不含结尾分号）。

export interface SqlStatement {
  sql: string;
  start: number;
  end: number;
}

export function splitSql(src: string): SqlStatement[] {
  const out: SqlStatement[] = [];
  let i = 0;
  let start = -1;
  const n = src.length;

  while (i < n) {
    const c = src[i];
    const c2 = i + 1 < n ? src[i + 1] : '';

    // 单行注释 --
    if (c === '-' && c2 === '-') {
      while (i < n && src[i] !== '\n') i++;
      continue;
    }
    // 单行注释 #
    if (c === '#') {
      while (i < n && src[i] !== '\n') i++;
      continue;
    }
    // 多行注释 /* ... */
    if (c === '/' && c2 === '*') {
      i += 2;
      while (i < n && !(src[i] === '*' && i + 1 < n && src[i + 1] === '/')) i++;
      if (i < n) i += 2; // 跳过 */
      continue;
    }
    // 字符串 / 标识符：' " ` [...]
    if (c === "'" || c === '"' || c === '`') {
      if (start < 0) start = i;
      const q = c;
      i++;
      while (i < n) {
        if (src[i] === q) {
          if (i + 1 < n && src[i + 1] === q) { i += 2; continue; }
          i++;
          break;
        }
        i++;
      }
      continue;
    }
    if (c === '[') {
      if (start < 0) start = i;
      i++;
      while (i < n && src[i] !== ']') i++;
      if (i < n) i++;
      continue;
    }
    // 分号：切分点
    if (c === ';') {
      if (start >= 0) {
        const chunk = src.slice(start, i);
        if (chunk.trim()) out.push({ sql: chunk, start, end: i });
      }
      start = -1;
      i++;
      continue;
    }
    // 空白：不影响 start 判定
    if (/\s/.test(c)) { i++; continue; }
    // 其他字符：首次遇到的非空字符标记 statement 起点
    if (start < 0) start = i;
    i++;
  }

  if (start >= 0) {
    const chunk = src.slice(start);
    if (chunk.trim()) out.push({ sql: chunk, start, end: n });
  }

  return out;
}

export function countStatements(src: string): number {
  return splitSql(src).length;
}

export function statementAtCursor(src: string, offset: number): number {
  const stmts = splitSql(src);
  for (let i = 0; i < stmts.length; i++) {
    if (offset >= stmts[i].start && offset <= stmts[i].end) return i;
  }
  return -1;
}

// 统计 SQL 中 `?` 参数占位符数量，忽略字符串字面量与注释里的问号
export function countParams(src: string): number {
  let n = 0;
  let i = 0;
  const L = src.length;
  while (i < L) {
    const c = src[i];
    const c2 = i + 1 < L ? src[i + 1] : '';
    if (c === '-' && c2 === '-') {
      while (i < L && src[i] !== '\n') i++;
      continue;
    }
    if (c === '#') {
      while (i < L && src[i] !== '\n') i++;
      continue;
    }
    if (c === '/' && c2 === '*') {
      i += 2;
      while (i < L && !(src[i] === '*' && i + 1 < L && src[i + 1] === '/')) i++;
      if (i < L) i += 2;
      continue;
    }
    if (c === "'" || c === '"' || c === '`') {
      const q = c;
      i++;
      while (i < L) {
        if (src[i] === q) {
          if (i + 1 < L && src[i + 1] === q) { i += 2; continue; }
          i++;
          break;
        }
        i++;
      }
      continue;
    }
    if (c === '[') {
      i++;
      while (i < L && src[i] !== ']') i++;
      if (i < L) i++;
      continue;
    }
    if (c === '?') n++;
    i++;
  }
  return n;
}
