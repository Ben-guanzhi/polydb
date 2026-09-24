// D4（阶段 D）Redis 整值写回的纯门禁逻辑：谁能被整值替换。
// 纯函数、无 IO、无 React，供 RedisBrowser 与单测复用。
// 三端同一语义：GUI `util::redis_write_block` / TUI `kvStartEdit`，门禁顺序必须一致
// （值未加载 → 只读连接 → 非 string），否则同一连接在不同前端会得到不同提示。
// 许可红线：仅参考 TablePro(AGPLv3) 的交互语义，实现全部自研，未移植其代码。
import type { RedisValue } from '../api';

export function redisWriteBlock(value: RedisValue | null, readOnly?: boolean): string | null {
  if (!value) return '先从左侧选择键并加载值';
  if (readOnly) return '连接标记为只读，不能写入';
  if (value.type !== 'string') {
    return `仅 string 支持整值写回，当前类型 ${value.type}；请用下方命令行（RPUSH/HSET …）`;
  }
  return null;
}
