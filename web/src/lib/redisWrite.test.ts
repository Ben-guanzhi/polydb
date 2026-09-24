import { describe, expect, it } from 'vitest';
import type { RedisValue } from '../api';
import { redisWriteBlock } from './redisWrite';

const val = (type: RedisValue['type'], value: RedisValue['value'] = 'x'): RedisValue => ({ type, value });

describe('redisWriteBlock', () => {
  it('已加载的 string 在可写连接上放行', () => {
    expect(redisWriteBlock(val('string', 'hello'))).toBeNull();
    expect(redisWriteBlock(val('string', 'hello'), false)).toBeNull();
  });

  it('值未加载先提示加载', () => {
    expect(redisWriteBlock(null)).toBe('先从左侧选择键并加载值');
    expect(redisWriteBlock(null, true)).toBe('先从左侧选择键并加载值');
  });

  it('只读门禁优先于类型门禁（与 TUI/GUI 提示顺序一致）', () => {
    expect(redisWriteBlock(val('list', ['a']), true)).toBe('连接标记为只读，不能写入');
  });

  it('非 string 提示走命令行并带上实际类型', () => {
    expect(redisWriteBlock(val('hash', { f: 'v' }))).toContain('当前类型 hash');
    expect(redisWriteBlock(val('zset', [{ member: 'm', score: 1 }]))).toContain('RPUSH/HSET');
  });
});
