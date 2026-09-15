import { useTranslation } from 'react-i18next';
import type { ProviderQuotaState } from '@/types';
import { formatDateTimeValue } from '@/utils/format';
import { QuotaMeter } from '../../components/QuotaMeter';
import type { QuotaClassMap } from '../../types';

interface ProviderQuotaBodyProps {
  quota: ProviderQuotaState;
  classes: QuotaClassMap;
}

const formatValue = (value: number): string =>
  Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/\.00$/, '');

export function ProviderQuotaBody({ quota, classes }: ProviderQuotaBodyProps) {
  const { t } = useTranslation();
  const resetCredits = quota.resetCredits?.filter((credit) => credit.available);
  const availableResetCount = resetCredits?.length ?? 0;

  return (
    <div>
      {(quota.plan || quota.balance !== undefined) && (
        <div className={classes.codexPlan}>
          {quota.plan && (
            <div className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{t('provider_quota.plan')}</span>
              <span className={classes.codexPlanValue}>{quota.plan}</span>
            </div>
          )}
          {quota.balance !== undefined && (
            <div className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{t('provider_quota.monthly_credits')}</span>
              <span className={classes.codexPlanValue}>
                {quota.balanceUnit === 'USD' ? '$' : ''}
                {formatValue(quota.balance)}
                {quota.balanceUnit && quota.balanceUnit !== 'USD' ? ` ${quota.balanceUnit}` : ''}
              </span>
            </div>
          )}
        </div>
      )}

      {resetCredits !== undefined || quota.resetCreditsError ? (
        <div className={classes.codexResetCredits}>
          <div className={classes.codexResetCreditsTitle}>
            {t('provider_quota.reset_credits_title', { count: availableResetCount })}
          </div>
          {resetCredits?.length ? (
            resetCredits.map((credit) => (
              <div key={credit.actionId} className={classes.codexResetCreditRow}>
                <span className={classes.codexResetCreditLabel}>
                  {t('provider_quota.reset_credit_row', {
                    quota: t(
                      credit.type === 'FIVE_HOUR'
                        ? 'provider_quota.reset_five_hour'
                        : 'provider_quota.reset_week'
                    ),
                    status: t(
                      credit.available
                        ? 'provider_quota.reset_credit_available'
                        : 'provider_quota.reset_credit_expired'
                    ),
                  })}
                </span>
                <span className={classes.codexResetCreditTime}>
                  {credit.expiresAtMs
                    ? t('provider_quota.reset_credit_expires_at', {
                        time: formatDateTimeValue(credit.expiresAtMs),
                      })
                    : t('provider_quota.reset_credit_expiry_unknown')}
                </span>
              </div>
            ))
          ) : quota.resetCreditsError ? (
            <div className={classes.codexResetCreditsError}>
              {t('provider_quota.reset_credits_load_failed', {
                message: quota.resetCreditsError,
              })}
            </div>
          ) : (
            <div className={classes.quotaMessage}>{t('provider_quota.reset_credits_empty')}</div>
          )}
        </div>
      ) : null}

      {quota.windows.map((window, index) => (
        <div key={window.id} className={classes.quotaRow}>
          <div className={classes.quotaRowHeader}>
            <span className={classes.quotaModel}>{window.label}</span>
            <span className={classes.quotaPercent}>
              {t('provider_quota.used_percent', {
                value: Math.round(window.usedPercent * 100) / 100,
              })}
            </span>
          </div>
          <QuotaMeter percent={window.remainingPercent} classes={classes} index={index} />
          <div className={classes.quotaMeta}>
            <span className={classes.quotaAmount}>
              {window.usedValue !== undefined && window.limitValue !== undefined
                ? t('provider_quota.used_value', {
                    used: formatValue(window.usedValue),
                    limit: formatValue(window.limitValue),
                    unit: window.unit || '',
                  })
                : t('provider_quota.remaining_percent', {
                    value: Math.round(window.remainingPercent * 100) / 100,
                  })}
            </span>
            {window.resetAtMs ? (
              <span className={classes.quotaReset}>
                {t('provider_quota.resets_at', {
                  time: formatDateTimeValue(window.resetAtMs),
                })}
              </span>
            ) : null}
          </div>
        </div>
      ))}
    </div>
  );
}
