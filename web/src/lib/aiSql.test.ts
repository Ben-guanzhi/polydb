import { describe, expect, it } from 'vitest';
import { chatCompletionsUrl, loadAiSettings, saveAiSettings } from './aiSettings';
import { buildMessages, extractSql } from './aiSql';

describe('buildMessages', () => {
  it('generate includes dialect/schema/table/request', () => {
    const m = buildMessages('generate', { dialect: 'postgres', schema: 'main', table: 'orders', request: '最近 7 天订单数' });
    expect(m[0].role).toBe('system');
    const u = m[1].content;
    expect(u).toContain('postgres');
    expect(u).toContain('main');
    expect(u).toContain('orders');
    expect(u).toContain('最近 7 天订单数');
  });

  it('explain/optimize embed the current SQL', () => {
    expect(buildMessages('explain', { currentSql: 'SELECT 1' })[1].content).toContain('SELECT 1');
    expect(buildMessages('optimize', { dialect: 'mysql', currentSql: 'SELECT * FROM t' })[1].content).toContain('SELECT * FROM t');
    // 无 dialect 时占位为「方言未知」
    expect(buildMessages('optimize', { currentSql: 'SELECT * FROM t' })[1].content).toContain('方言未知');
  });
});

describe('extractSql', () => {
  it('pulls fenced sql block', () => {
    expect(extractSql('好的：\n```sql\nSELECT 1;\n```\n完毕')).toBe('SELECT 1;');
  });
  it('accepts bare sql', () => {
    expect(extractSql('  SELECT a FROM b;\n')).toBe('SELECT a FROM b;');
  });
  it('no-fence explanation returns trimmed text', () => {
    expect(extractSql('  这条 SQL 统计订单。\n')).toBe('这条 SQL 统计订单。');
  });
});

describe('aiSettings', () => {
  it('defaults and roundtrip', () => {
    const d = loadAiSettings();
    expect(d.model.length).toBeGreaterThan(0);
    saveAiSettings({ baseUrl: 'http://x/v1', model: 'm', apiKey: 'k', temperature: 0.5 });
    const r = loadAiSettings();
    expect(r.baseUrl).toBe('http://x/v1');
    expect(r.apiKey).toBe('k');
    // 恢复默认避免污染其他用例
    saveAiSettings({ baseUrl: 'https://api.openai.com/v1', model: 'gpt-4o-mini', apiKey: '', temperature: 0.2 });
  });
  it('chatCompletionsUrl appends or keeps path', () => {
    expect(chatCompletionsUrl('https://h/v1')).toBe('https://h/v1/chat/completions');
    expect(chatCompletionsUrl('https://h/v1/')).toBe('https://h/v1/chat/completions');
    expect(chatCompletionsUrl('https://h/v1/chat/completions')).toBe('https://h/v1/chat/completions');
  });
});
