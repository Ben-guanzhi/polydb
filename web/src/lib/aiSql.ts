// M17：AI 助手的提示词构建与响应解析（纯函数，可单测）+ OpenAI 兼容调用。
import type { AiSettings } from './aiSettings';
import { chatCompletionsUrl } from './aiSettings';

export type AiMode = 'generate' | 'explain' | 'optimize';

export interface AiContext {
  dialect?: string;
  schema?: string;
  table?: string;
  /** 当前编辑器 SQL（explain/optimize 用） */
  currentSql?: string;
  /** 自然语言需求（generate 用） */
  request?: string;
}

export interface AiChatMessage {
  role: 'system' | 'user' | 'assistant';
  content: string;
}

const SYSTEM = [
  '你是 polydb 数据库客户端的 SQL 助手。',
  '只输出与任务相关的内容：生成 SQL 时只输出 SQL（可用 ```sql 代码块包裹，不要解释）；',
  '解释/优化时用简洁的中文分点说明。',
].join(' ');

export function buildMessages(mode: AiMode, ctx: AiContext): AiChatMessage[] {
  const dialect = ctx.dialect ?? 'SQL（方言未知）';
  const msgs: AiChatMessage[] = [{ role: 'system', content: SYSTEM }];
  let user = '';
  switch (mode) {
    case 'generate':
      user =
        `目标方言：${dialect}。\n` +
        (ctx.schema ? `schema：${ctx.schema}\n` : '') +
        (ctx.table ? `关注表：${ctx.table}\n` : '') +
        `需求：${ctx.request ?? ''}\n请生成对应 SQL。`;
      break;
    case 'explain':
      user = `目标方言：${dialect}。\n请解释下面 SQL 的作用、涉及表/列与潜在问题：\n${ctx.currentSql ?? ''}`;
      break;
    case 'optimize':
      user = `目标方言：${dialect}。\n请优化下面 SQL（可读性/性能/索引提示），先给结论再给改写：\n${ctx.currentSql ?? ''}`;
      break;
  }
  msgs.push({ role: 'user', content: user });
  return msgs;
}

/** 从模型输出中提取 SQL：优先取 ```sql 代码块，其次整体去壳。 */
export function extractSql(text: string): string {
  const fence = text.match(/```(?:sql)?\s*\n([\s\S]*?)```/i);
  if (fence && fence[1]) return fence[1].trim();
  // 无代码块：若整体像 SQL（去掉前后空白/解释行）则返回原文
  return text.trim();
}

export interface AiCallResult {
  content: string;
}

/** 调用 OpenAI 兼容 chat/completions（浏览器直连，密钥不经过 polydb server）。 */
export async function chatCompletion(
  settings: AiSettings,
  messages: AiChatMessage[],
  signal?: AbortSignal,
): Promise<AiCallResult> {
  const res = await fetch(chatCompletionsUrl(settings.baseUrl), {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${settings.apiKey}`,
    },
    body: JSON.stringify({
      model: settings.model,
      messages,
      temperature: settings.temperature,
    }),
    signal,
  });
  if (!res.ok) {
    const body = await res.text().catch(() => '');
    throw new Error(`AI 请求失败 ${res.status}: ${body.slice(0, 300)}`);
  }
  const data = (await res.json()) as { choices?: Array<{ message?: { content?: string } }> };
  const content = data.choices?.[0]?.message?.content ?? '';
  return { content };
}
