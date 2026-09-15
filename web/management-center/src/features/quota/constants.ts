import type { QuotaProviderType } from './providers/types';

/** Tab order also defines provider grouping on the All tab. */
export const QUOTA_TAB_ORDER: readonly QuotaProviderType[] = [
  'claude',
  'antigravity',
  'codex',
  'xai',
  'kimi',
  'commandcode',
  'opencode',
  'zhipu',
  'devin',
];

export type QuotaTabId = 'all' | QuotaProviderType;

/** Page size also bounds upstream concurrency for Refresh all. */
export const QUOTA_PAGE_SIZE = 20;

/** Successful quota snapshots remain fresh for five minutes across reloads. */
export const QUOTA_CACHE_TTL_MS = 5 * 60 * 1000;

/** Card sorting: provider grouping by default, or soonest recovery first. */
export const QUOTA_SORT_MODES = ['default', 'soonest'] as const;

export type QuotaSortMode = (typeof QUOTA_SORT_MODES)[number];

/** Matches useRevealGroup's GROUP_MAX_TOTAL entrance budget. */
export const CARD_ENTRANCE_BUDGET_MS = 360;
