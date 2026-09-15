/**
 * Quota provider adapters combine React-free data modules with *QuotaBody renderers.
 *
 * Pages consume the type-erased QuotaAdapter view. Each data.ts export retains
 * the concrete state type, following the narrow-cast pattern in AuthFileQuotaSection.
 */

import type { ComponentType } from 'react';
import type { TFunction } from 'i18next';
import { useQuotaStore } from '@/stores';
import type { AuthFileItem } from '@/types';
import type { QuotaBodyProps } from '../types';
import type { QuotaProviderType, QuotaResetAction, QuotaStore } from './types';
import { ANTIGRAVITY_CONFIG } from './antigravity/data';
import { AntigravityQuotaBody } from './antigravity/AntigravityQuotaBody';
import { CLAUDE_CONFIG } from './claude/data';
import { ClaudeQuotaBody } from './claude/ClaudeQuotaBody';
import { CODEX_CONFIG } from './codex/data';
import { CodexQuotaBody } from './codex/CodexQuotaBody';
import { KIMI_CONFIG } from './kimi/data';
import { KimiQuotaBody } from './kimi/KimiQuotaBody';
import { XAI_CONFIG } from './xai/data';
import { XaiQuotaBody } from './xai/XaiQuotaBody';
import {
  COMMANDCODE_CONFIG,
  DEVIN_CONFIG,
  OPENCODE_CONFIG,
  ZHIPU_CONFIG,
} from './providerApi/data';
import { ProviderQuotaBody } from './providerApi/ProviderQuotaBody';

/** Common structural subset shared by every provider quota state. */
export interface QuotaCardState {
  status: 'idle' | 'loading' | 'success' | 'error';
  error?: string;
  errorStatus?: number;
  fetchedAt?: number;
}

export interface QuotaAdapter {
  type: QuotaProviderType;
  i18nPrefix: string;
  filterFn: (file: AuthFileItem) => boolean;
  fetchQuota: (file: AuthFileItem, t: TFunction) => Promise<unknown>;
  resetQuota?: (file: AuthFileItem, t: TFunction, actionId?: string) => Promise<unknown>;
  canResetQuota?: (quota: QuotaCardState) => boolean;
  getResetActions?: (quota: QuotaCardState, t: TFunction) => QuotaResetAction[];
  storeSelector: (state: QuotaStore) => Record<string, QuotaCardState>;
  storeSetter: keyof QuotaStore;
  buildLoadingState: () => QuotaCardState;
  buildSuccessState: (data: unknown) => QuotaCardState;
  buildErrorState: (message: string, status?: number) => QuotaCardState;
  Body: ComponentType<QuotaBodyProps<QuotaCardState>>;
}

export const QUOTA_ADAPTERS: Record<QuotaProviderType, QuotaAdapter> = {
  antigravity: {
    ...ANTIGRAVITY_CONFIG,
    Body: AntigravityQuotaBody,
  } as unknown as QuotaAdapter,
  claude: { ...CLAUDE_CONFIG, Body: ClaudeQuotaBody } as unknown as QuotaAdapter,
  codex: { ...CODEX_CONFIG, Body: CodexQuotaBody } as unknown as QuotaAdapter,
  kimi: { ...KIMI_CONFIG, Body: KimiQuotaBody } as unknown as QuotaAdapter,
  xai: { ...XAI_CONFIG, Body: XaiQuotaBody } as unknown as QuotaAdapter,
  commandcode: { ...COMMANDCODE_CONFIG, Body: ProviderQuotaBody } as unknown as QuotaAdapter,
  opencode: { ...OPENCODE_CONFIG, Body: ProviderQuotaBody } as unknown as QuotaAdapter,
  zhipu: { ...ZHIPU_CONFIG, Body: ProviderQuotaBody } as unknown as QuotaAdapter,
  devin: { ...DEVIN_CONFIG, Body: ProviderQuotaBody } as unknown as QuotaAdapter,
};

export type QuotaMapUpdater = (
  updater: (prev: Record<string, QuotaCardState>) => Record<string, QuotaCardState>
) => void;

/** Read the adapter's store setter without creating a subscription. */
export const getQuotaSetter = (adapter: QuotaAdapter): QuotaMapUpdater =>
  useQuotaStore.getState()[adapter.storeSetter] as unknown as QuotaMapUpdater;

/** Read the adapter's cached quota snapshot without creating a subscription. */
export const getQuotaMap = (adapter: QuotaAdapter): Record<string, QuotaCardState> =>
  adapter.storeSelector(useQuotaStore.getState() as unknown as QuotaStore);

export type { QuotaResetAction } from './types';
