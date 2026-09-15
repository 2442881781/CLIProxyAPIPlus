/**
 * Quota provider data-layer contract.
 *
 * data.ts modules only fetch data and build state. They do not import React or
 * SCSS, so bun:test can consume them directly. Sibling *QuotaBody components render them.
 */

import type { TFunction } from 'i18next';
import type {
  AntigravityQuotaState,
  AuthFileItem,
  ClaudeQuotaState,
  CodexQuotaState,
  KimiQuotaState,
  ProviderQuotaState,
  XaiQuotaState,
} from '@/types';

export type QuotaUpdater<T> = T | ((prev: T) => T);

export type QuotaProviderType =
  | 'antigravity'
  | 'claude'
  | 'codex'
  | 'kimi'
  | 'xai'
  | 'commandcode'
  | 'opencode'
  | 'zhipu'
  | 'devin';

/** Structural contract used by storeSelector and storeSetter. */
export interface QuotaStore {
  antigravityQuota: Record<string, AntigravityQuotaState>;
  claudeQuota: Record<string, ClaudeQuotaState>;
  codexQuota: Record<string, CodexQuotaState>;
  kimiQuota: Record<string, KimiQuotaState>;
  xaiQuota: Record<string, XaiQuotaState>;
  commandcodeQuota: Record<string, ProviderQuotaState>;
  opencodeQuota: Record<string, ProviderQuotaState>;
  zhipuQuota: Record<string, ProviderQuotaState>;
  devinQuota: Record<string, ProviderQuotaState>;
  setAntigravityQuota: (updater: QuotaUpdater<Record<string, AntigravityQuotaState>>) => void;
  setClaudeQuota: (updater: QuotaUpdater<Record<string, ClaudeQuotaState>>) => void;
  setCodexQuota: (updater: QuotaUpdater<Record<string, CodexQuotaState>>) => void;
  setKimiQuota: (updater: QuotaUpdater<Record<string, KimiQuotaState>>) => void;
  setXaiQuota: (updater: QuotaUpdater<Record<string, XaiQuotaState>>) => void;
  setCommandcodeQuota: (updater: QuotaUpdater<Record<string, ProviderQuotaState>>) => void;
  setOpencodeQuota: (updater: QuotaUpdater<Record<string, ProviderQuotaState>>) => void;
  setZhipuQuota: (updater: QuotaUpdater<Record<string, ProviderQuotaState>>) => void;
  setDevinQuota: (updater: QuotaUpdater<Record<string, ProviderQuotaState>>) => void;
  clearQuotaCache: () => void;
}

export interface QuotaResetAction {
  id?: string;
  buttonLabel: string;
  confirmTitle?: string;
  confirmMessage?: string;
  confirmButton?: string;
  successMessage?: string;
}

export interface QuotaProviderData<TState, TData> {
  type: QuotaProviderType;
  i18nPrefix: string;
  filterFn: (file: AuthFileItem) => boolean;
  fetchQuota: (file: AuthFileItem, t: TFunction) => Promise<TData>;
  resetQuota?: (file: AuthFileItem, t: TFunction, actionId?: string) => Promise<TData>;
  canResetQuota?: (quota: TState) => boolean;
  getResetActions?: (quota: TState, t: TFunction) => QuotaResetAction[];
  storeSelector: (state: QuotaStore) => Record<string, TState>;
  storeSetter: keyof QuotaStore;
  buildLoadingState: () => TState;
  buildSuccessState: (data: TData) => TState;
  buildErrorState: (message: string, status?: number) => TState;
}
