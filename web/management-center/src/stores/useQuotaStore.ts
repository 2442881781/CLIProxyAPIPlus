/**
 * Quota snapshots are server-derived runtime data and intentionally remain
 * in-memory. Persistent snapshots are owned by the backend.
 */

import { create } from 'zustand';
import type {
  AntigravityQuotaState,
  ClaudeQuotaState,
  CodexQuotaState,
  KimiQuotaState,
  ProviderQuotaState,
  XaiQuotaState,
} from '@/types';

type QuotaUpdater<T> = T | ((prev: T) => T);

interface QuotaStoreState {
  cacheGeneration: number;
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

const resolveUpdater = <T>(updater: QuotaUpdater<T>, prev: T): T => {
  if (typeof updater === 'function') {
    return (updater as (value: T) => T)(prev);
  }
  return updater;
};

export const useQuotaStore = create<QuotaStoreState>()((set) => ({
  cacheGeneration: 0,
  antigravityQuota: {},
  claudeQuota: {},
  codexQuota: {},
  kimiQuota: {},
  xaiQuota: {},
  commandcodeQuota: {},
  opencodeQuota: {},
  zhipuQuota: {},
  devinQuota: {},
  setAntigravityQuota: (updater) =>
    set((state) => ({ antigravityQuota: resolveUpdater(updater, state.antigravityQuota) })),
  setClaudeQuota: (updater) =>
    set((state) => ({ claudeQuota: resolveUpdater(updater, state.claudeQuota) })),
  setCodexQuota: (updater) =>
    set((state) => ({ codexQuota: resolveUpdater(updater, state.codexQuota) })),
  setKimiQuota: (updater) =>
    set((state) => ({ kimiQuota: resolveUpdater(updater, state.kimiQuota) })),
  setXaiQuota: (updater) =>
    set((state) => ({ xaiQuota: resolveUpdater(updater, state.xaiQuota) })),
  setCommandcodeQuota: (updater) =>
    set((state) => ({ commandcodeQuota: resolveUpdater(updater, state.commandcodeQuota) })),
  setOpencodeQuota: (updater) =>
    set((state) => ({ opencodeQuota: resolveUpdater(updater, state.opencodeQuota) })),
  setZhipuQuota: (updater) =>
    set((state) => ({ zhipuQuota: resolveUpdater(updater, state.zhipuQuota) })),
  setDevinQuota: (updater) =>
    set((state) => ({ devinQuota: resolveUpdater(updater, state.devinQuota) })),
  clearQuotaCache: () =>
    set((state) => ({
      cacheGeneration: state.cacheGeneration + 1,
      antigravityQuota: {},
      claudeQuota: {},
      codexQuota: {},
      kimiQuota: {},
      xaiQuota: {},
      commandcodeQuota: {},
      opencodeQuota: {},
      zhipuQuota: {},
      devinQuota: {},
    })),
}));

export const captureQuotaCacheGeneration = (): number => useQuotaStore.getState().cacheGeneration;

export const commitIfQuotaCacheCurrent = (generation: number, commit: () => void): boolean => {
  if (useQuotaStore.getState().cacheGeneration !== generation) return false;
  commit();
  return true;
};

if (typeof window !== 'undefined') {
  try {
    window.localStorage.removeItem('cli-proxy-quota-cache');
  } catch {
    // Storage can be unavailable in browser privacy modes.
  }
}
