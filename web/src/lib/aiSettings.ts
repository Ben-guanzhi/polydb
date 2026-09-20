// M17：BYOK AI 助手配置。密钥只存在浏览器 localStorage，绝不经过 polydb server
//（服务端不持有 LLM key，红线）。端点为任意 OpenAI 兼容的 /v1/chat/completions。
export interface AiSettings {
  /** OpenAI 兼容 base，如 https://api.openai.com/v1 或本地/自建网关 */
  baseUrl: string;
  model: string;
  apiKey: string;
  temperature: number;
}

export const DEFAULT_AI: AiSettings = {
  baseUrl: 'https://api.openai.com/v1',
  model: 'gpt-4o-mini',
  apiKey: '',
  temperature: 0.2,
};

const KEY = 'polydb.ai.v1';

export function loadAiSettings(): AiSettings {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return { ...DEFAULT_AI };
    const o = JSON.parse(raw);
    const out = { ...DEFAULT_AI };
    if (o && typeof o === 'object') {
      if (typeof o.baseUrl === 'string') out.baseUrl = o.baseUrl;
      if (typeof o.model === 'string') out.model = o.model;
      if (typeof o.apiKey === 'string') out.apiKey = o.apiKey;
      if (typeof o.temperature === 'number' && Number.isFinite(o.temperature)) out.temperature = o.temperature;
    }
    return out;
  } catch {
    return { ...DEFAULT_AI };
  }
}

export function saveAiSettings(s: AiSettings): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(s));
  } catch {
    /* ignore quota / privacy mode */
  }
}

export function hasAiConfig(s: AiSettings): boolean {
  return s.baseUrl.trim() !== '' && s.apiKey.trim() !== '';
}

/** 规范化 chat/completions 完整 URL：baseUrl 已含 /chat/completions 则原样，否则拼接。 */
export function chatCompletionsUrl(baseUrl: string): string {
  const base = baseUrl.trim().replace(/\/+$/, '');
  if (base.endsWith('/chat/completions')) return base;
  return `${base}/chat/completions`;
}
