import { useCallback, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useNotificationStore,
  useQuotaStore,
} from '@/stores';
import type { AuthFileItem } from '@/types';
import { getStatusFromError, resolveQuotaErrorMessage } from '@/utils/quota';
import { isRuntimeOnlyAuthFile, type QuotaProviderType } from '@/features/authFiles/constants';
import { Button } from '@/components/ui/Button';
import { IconRefreshCw } from '@/components/ui/icons';
import { bindQuotaClasses } from '@/features/quota/types';
import {
  QUOTA_ADAPTERS,
  type QuotaCardState,
  type QuotaResetAction,
} from '@/features/quota/providers';
import styles from './AuthFileQuota.module.scss';

/** Bind the compact auth-file card styles to the fail-loud quota class contract. */
const compactQuotaClasses = bindQuotaClasses(styles, 'AuthFileQuota.module.scss');

const assertNever = (value: never): never => {
  throw new Error(`Unsupported quota type: ${value}`);
};

type QuotaMapUpdater = (
  updater: (prev: Record<string, QuotaCardState>) => Record<string, QuotaCardState>
) => void;

export type AuthFileQuotaSectionProps = {
  file: AuthFileItem;
  quotaType: QuotaProviderType;
  disableControls: boolean;
};

export function AuthFileQuotaSection(props: AuthFileQuotaSectionProps) {
  const { file, quotaType, disableControls } = props;
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const [resettingQuota, setResettingQuota] = useState(false);
  const adapter = QUOTA_ADAPTERS[quotaType];

  const quota = useQuotaStore((state) => {
    if (quotaType === 'antigravity')
      return state.antigravityQuota[file.name] as QuotaCardState | undefined;
    if (quotaType === 'claude') return state.claudeQuota[file.name] as QuotaCardState | undefined;
    if (quotaType === 'codex') return state.codexQuota[file.name] as QuotaCardState | undefined;
    if (quotaType === 'kimi') return state.kimiQuota[file.name] as QuotaCardState | undefined;
    if (quotaType === 'xai') return state.xaiQuota[file.name] as QuotaCardState | undefined;
    if (quotaType === 'commandcode')
      return state.commandcodeQuota[file.name] as QuotaCardState | undefined;
    if (quotaType === 'opencode')
      return state.opencodeQuota[file.name] as QuotaCardState | undefined;
    if (quotaType === 'zhipu') return state.zhipuQuota[file.name] as QuotaCardState | undefined;
    if (quotaType === 'devin') return state.devinQuota[file.name] as QuotaCardState | undefined;
    return assertNever(quotaType);
  });

  const updateQuotaState = useQuotaStore(
    (state) => state[adapter.storeSetter] as unknown as QuotaMapUpdater
  );

  const refreshQuotaForFile = useCallback(async () => {
    if (disableControls) return;
    if (isRuntimeOnlyAuthFile(file)) return;
    if (file.disabled) return;
    if (quota?.status === 'loading') return;

    const cacheGeneration = captureQuotaCacheGeneration();

    updateQuotaState((prev) => ({
      ...prev,
      [file.name]: adapter.buildLoadingState(),
    }));

    try {
      const data = await adapter.fetchQuota(file, t);
      commitIfQuotaCacheCurrent(cacheGeneration, () => {
        updateQuotaState((prev) => ({
          ...prev,
          [file.name]: adapter.buildSuccessState(data),
        }));
        showNotification(t('auth_files.quota_refresh_success', { name: file.name }), 'success');
      });
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : t('common.unknown_error');
      const status = getStatusFromError(err);
      commitIfQuotaCacheCurrent(cacheGeneration, () => {
        updateQuotaState((prev) => ({
          ...prev,
          [file.name]: adapter.buildErrorState(message, status),
        }));
        showNotification(
          t('auth_files.quota_refresh_failed', { name: file.name, message }),
          'error'
        );
      });
    }
  }, [adapter, disableControls, file, quota?.status, showNotification, t, updateQuotaState]);

  const resetQuotaForFile = useCallback(
    (action?: QuotaResetAction) => {
      if (disableControls) return;
      if (isRuntimeOnlyAuthFile(file)) return;
      if (file.disabled) return;
      if (quota?.status === 'loading') return;
      if (resettingQuota) return;

      const resetQuota = adapter.resetQuota;
      if (!resetQuota) return;

      showConfirmation({
        title: action?.confirmTitle ?? t('codex_quota.reset_confirm_title'),
        message:
          action?.confirmMessage ?? t('codex_quota.reset_confirm_message', { name: file.name }),
        confirmText: action?.confirmButton ?? t('codex_quota.reset_confirm_button'),
        variant: 'primary',
        onConfirm: async () => {
          const cacheGeneration = captureQuotaCacheGeneration();
          setResettingQuota(true);
          try {
            const data = await resetQuota(file, t, action?.id);
            commitIfQuotaCacheCurrent(cacheGeneration, () => {
              updateQuotaState((prev) => ({
                ...prev,
                [file.name]: adapter.buildSuccessState(data),
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
            setResettingQuota(false);
          }
        },
      });
    },
    [
      adapter,
      disableControls,
      file,
      quota?.status,
      resettingQuota,
      showConfirmation,
      showNotification,
      t,
      updateQuotaState,
    ]
  );

  const quotaStatus = quota?.status ?? 'idle';
  const canRefreshQuota = !disableControls && !file.disabled && !resettingQuota;
  const canUseResetQuota = canRefreshQuota && quotaStatus !== 'loading';
  const resetActions: QuotaResetAction[] =
    adapter.resetQuota && quota
      ? (adapter.getResetActions?.(quota, t) ??
        (adapter.canResetQuota?.(quota) ? [{ buttonLabel: t('codex_quota.reset_button') }] : []))
      : [];
  const quotaErrorMessage = resolveQuotaErrorMessage(
    t,
    quota?.errorStatus,
    quota?.error || t('common.unknown_error')
  );

  return (
    <div className={styles.quotaSection}>
      {quotaStatus === 'loading' ? (
        <div className={styles.quotaMessage}>{t(`${adapter.i18nPrefix}.loading`)}</div>
      ) : quotaStatus === 'idle' ? (
        <button
          type="button"
          className={`${styles.quotaMessage} ${styles.quotaMessageAction}`}
          onClick={() => void refreshQuotaForFile()}
          disabled={!canRefreshQuota}
        >
          {t(`${adapter.i18nPrefix}.idle`)}
        </button>
      ) : quotaStatus === 'error' ? (
        <div className={styles.quotaError}>
          {t(`${adapter.i18nPrefix}.load_failed`, {
            message: quotaErrorMessage,
          })}
        </div>
      ) : quota ? (
        <adapter.Body quota={quota} classes={compactQuotaClasses} />
      ) : (
        <div className={styles.quotaMessage}>{t(`${adapter.i18nPrefix}.idle`)}</div>
      )}
      {quotaStatus !== 'idle' && resetActions.length > 0 && (
        <div className={styles.quotaCardActions}>
          {resetActions.map((action, index) => (
            <Button
              key={action.id ?? `reset-${index}`}
              type="button"
              variant="secondary"
              size="sm"
              className={styles.quotaResetCreditButton}
              onClick={() => resetQuotaForFile(action)}
              disabled={!canUseResetQuota}
              loading={resettingQuota}
              title={action.buttonLabel}
              aria-label={action.buttonLabel}
            >
              {!resettingQuota && <IconRefreshCw size={14} />}
              {action.buttonLabel}
            </Button>
          ))}
        </div>
      )}
    </div>
  );
}
