/**
 * Per-card quota actions: refresh and consume a provider reset credit.
 * Keeps confirmation, re-entry guards, generation-guarded commits, and notifications
 * centralized while provider adapters supply the request and action copy.
 */

import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useNotificationStore,
} from '@/stores';
import type { AuthFileItem } from '@/types';
import { getStatusFromError } from '@/utils/quota';
import {
  getQuotaMap,
  getQuotaSetter,
  type QuotaAdapter,
  type QuotaCardState,
  type QuotaResetAction,
} from '../providers';

const getQuotaState = (adapter: QuotaAdapter, name: string): QuotaCardState | undefined =>
  getQuotaMap(adapter)[name];

export function useQuotaActions(disableControls: boolean) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const [resettingQuotaName, setResettingQuotaName] = useState<string | null>(null);

  const refreshQuota = useCallback(
    async (file: AuthFileItem, adapter: QuotaAdapter) => {
      if (disableControls || file.disabled) return;
      if (resettingQuotaName === file.name) return;
      if (getQuotaState(adapter, file.name)?.status === 'loading') return;
      const cacheGeneration = captureQuotaCacheGeneration();
      const setQuota = getQuotaSetter(adapter);

      setQuota((prev) => ({
        ...prev,
        [file.name]: adapter.buildLoadingState(),
      }));

      try {
        const data = await adapter.fetchQuota(file, t);
        commitIfQuotaCacheCurrent(cacheGeneration, () => {
          setQuota((prev) => ({
            ...prev,
            [file.name]: { ...adapter.buildSuccessState(data), fetchedAt: Date.now() },
          }));
          showNotification(t('auth_files.quota_refresh_success', { name: file.name }), 'success');
        });
      } catch (err: unknown) {
        const message = err instanceof Error ? err.message : t('common.unknown_error');
        const status = getStatusFromError(err);
        commitIfQuotaCacheCurrent(cacheGeneration, () => {
          setQuota((prev) => ({
            ...prev,
            [file.name]: adapter.buildErrorState(message, status),
          }));
          showNotification(
            t('auth_files.quota_refresh_failed', { name: file.name, message }),
            'error'
          );
        });
      }
    },
    [disableControls, resettingQuotaName, showNotification, t]
  );

  const resetQuota = useCallback(
    (file: AuthFileItem, adapter: QuotaAdapter, action?: QuotaResetAction) => {
      const resetQuotaFn = adapter.resetQuota;
      if (!resetQuotaFn) return;
      if (disableControls || file.disabled) return;
      if (getQuotaState(adapter, file.name)?.status === 'loading') return;
      if (resettingQuotaName === file.name) return;

      showConfirmation({
        title: action?.confirmTitle ?? t('codex_quota.reset_confirm_title'),
        message:
          action?.confirmMessage ?? t('codex_quota.reset_confirm_message', { name: file.name }),
        confirmText: action?.confirmButton ?? t('codex_quota.reset_confirm_button'),
        variant: 'primary',
        onConfirm: async () => {
          const cacheGeneration = captureQuotaCacheGeneration();
          const setQuota = getQuotaSetter(adapter);
          setResettingQuotaName(file.name);
          try {
            const data = await resetQuotaFn(file, t, action?.id);
            commitIfQuotaCacheCurrent(cacheGeneration, () => {
              setQuota((prev) => ({
                ...prev,
                [file.name]: { ...adapter.buildSuccessState(data), fetchedAt: Date.now() },
              }));
              showNotification(
                action?.successMessage ?? t('codex_quota.reset_success', { name: file.name }),
                'success'
              );
            });
          } catch (err: unknown) {
            const message = err instanceof Error ? err.message : t('common.unknown_error');
            commitIfQuotaCacheCurrent(cacheGeneration, () => {
              showNotification(
                action
                  ? t('provider_quota.reset_failed', { message })
                  : t('codex_quota.reset_failed', { name: file.name, message }),
                'error'
              );
            });
          } finally {
            setResettingQuotaName((current) => (current === file.name ? null : current));
          }
        },
      });
    },
    [disableControls, resettingQuotaName, showConfirmation, showNotification, t]
  );

  return { resettingQuotaName, refreshQuota, resetQuota };
}
