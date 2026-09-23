/**
 * Web search provider key pool (Tavily / Exa / Firecrawl) management API.
 */

import { apiClient } from './client';

export const SEARCH_PROVIDERS = ['tavily', 'exa', 'firecrawl'] as const;
export type SearchProvider = (typeof SEARCH_PROVIDERS)[number];

export interface SearchKeyEntry {
  provider: string;
  apiKey: string;
  label: string;
  baseUrl: string;
  proxyUrl: string;
  disabled: boolean;
}

export interface SearchKeyStatus {
  id: string;
  index: number;
  provider: string;
  maskedKey: string;
  /** Epoch milliseconds; 0 when the key is not cooling down. */
  cooldownUntil: number;
  lastStatus: number;
  requests: number;
  failures: number;
}

type RawRecord = Record<string, unknown>;

const isRecord = (value: unknown): value is RawRecord =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const str = (value: unknown): string => (typeof value === 'string' ? value : '');

const num = (value: unknown): number =>
  typeof value === 'number' && Number.isFinite(value) ? value : 0;

const timestamp = (value: unknown): number => {
  if (typeof value !== 'string' || !value) return 0;
  const parsed = Date.parse(value);
  // Go serializes zero times as 0001-01-01T00:00:00Z.
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
};

export const parseSearchKeys = (data: unknown): SearchKeyEntry[] => {
  const list = isRecord(data) ? data['search-api-key'] : undefined;
  if (!Array.isArray(list)) return [];
  return list.filter(isRecord).flatMap((item) => {
    const apiKey = str(item['api-key']);
    const provider = str(item.provider);
    if (!apiKey || !provider) return [];
    return [
      {
        provider,
        apiKey,
        label: str(item.label),
        baseUrl: str(item['base-url']),
        proxyUrl: str(item['proxy-url']),
        disabled: item.disabled === true,
      },
    ];
  });
};

export const toSearchKeyPayload = (entry: SearchKeyEntry): RawRecord => {
  const payload: RawRecord = {
    provider: entry.provider.trim().toLowerCase(),
    'api-key': entry.apiKey.trim(),
  };
  const optional: Array<[string, string]> = [
    ['label', entry.label],
    ['base-url', entry.baseUrl],
    ['proxy-url', entry.proxyUrl],
  ];
  for (const [field, value] of optional) {
    if (value.trim()) payload[field] = value.trim();
  }
  if (entry.disabled) payload.disabled = true;
  return payload;
};

export const parseSearchKeyStatuses = (data: unknown): SearchKeyStatus[] => {
  const list = isRecord(data) ? data.keys : undefined;
  if (!Array.isArray(list)) return [];
  return list.filter(isRecord).map((item) => ({
    id: str(item.id),
    index: num(item.index),
    provider: str(item.provider),
    maskedKey: str(item['masked-key']),
    cooldownUntil: timestamp(item['cooldown-until']),
    lastStatus: num(item['last-status']),
    requests: num(item.requests),
    failures: num(item.failures),
  }));
};

export const searchKeysApi = {
  async list(): Promise<SearchKeyEntry[]> {
    return parseSearchKeys(await apiClient.get<unknown>('/search-api-key'));
  },

  async status(): Promise<SearchKeyStatus[]> {
    return parseSearchKeyStatuses(await apiClient.get<unknown>('/search-api-key/status'));
  },

  replace: (entries: SearchKeyEntry[]) =>
    apiClient.put('/search-api-key', entries.map(toSearchKeyPayload)),

  update: (index: number, entry: SearchKeyEntry) =>
    apiClient.patch('/search-api-key', {
      index,
      value: {
        label: '',
        'base-url': '',
        'proxy-url': '',
        disabled: false,
        ...toSearchKeyPayload(entry),
      },
    }),

  remove: (index: number) => apiClient.delete(`/search-api-key?index=${index}`),

  resetCooldown: (target: { provider: string; apiKey: string } | 'all') =>
    apiClient.post(
      '/search-api-key/reset-cooldown',
      target === 'all' ? { all: true } : { provider: target.provider, 'api-key': target.apiKey }
    ),
};
