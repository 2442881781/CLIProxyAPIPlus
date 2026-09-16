import { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { IconAlertTriangle, IconRefreshCw, IconSearch } from '@/components/ui/icons';
import { getAuthFileIcon, getTypeLabel } from '@/features/authFiles/constants';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usageMonitorApi } from '@/services/api';
import { useAuthStore, useThemeStore } from '@/stores';
import type { UsageMonitorSnapshot } from '@/types';
import { formatCompactNumber, formatDateTimeValue, formatPercent } from '@/utils/format';
import { filterUsageProviders, usageSuccessRate } from './logic';
import { KeyUsageTab } from './KeyUsageTab';
import { GroupUsageTab } from './GroupUsageTab';
import styles from './UsageMonitorPage.module.scss';

const AUTO_REFRESH_MS = 10_000;
const DASH = '—';

type UsageTab = 'overview' | 'keys' | 'groups';

const formatTokenValue = (value: number): string =>
  value < 100_000 ? value.toLocaleString() : formatCompactNumber(value);

export function UsageMonitorPage() {
  const { t } = useTranslation();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const [snapshot, setSnapshot] = useState<UsageMonitorSnapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [providerFilter, setProviderFilter] = useState('all');
  const [search, setSearch] = useState('');
  const [tab, setTab] = useState<UsageTab>('overview');
  const deferredSearch = useDeferredValue(search);
  const loadingRef = useRef(false);

  const loadUsage = useCallback(async () => {
    if (!connected || loadingRef.current) return;
    loadingRef.current = true;
    setError('');
    try {
      setSnapshot(await usageMonitorApi.getSnapshot());
    } catch (loadError: unknown) {
      setError(loadError instanceof Error ? loadError.message : t('common.unknown_error'));
    } finally {
      loadingRef.current = false;
      setLoading(false);
    }
  }, [connected, t]);

  useHeaderRefresh(loadUsage, connected);

  useEffect(() => {
    if (!connected) {
      setLoading(false);
      return;
    }
    void loadUsage();
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void loadUsage();
    }, AUTO_REFRESH_MS);
    return () => window.clearInterval(timer);
  }, [connected, loadUsage]);

  const providerOptions = useMemo(
    () => [
      { value: 'all', label: t('usage_monitor.all_providers') },
      ...(snapshot?.providers ?? []).map((provider) => ({
        value: provider.provider,
        label: getTypeLabel(t, provider.provider),
      })),
    ],
    [snapshot?.providers, t]
  );

  useEffect(() => {
    if (!providerOptions.some((option) => option.value === providerFilter)) {
      setProviderFilter('all');
    }
  }, [providerFilter, providerOptions]);

  const visibleProviders = useMemo(
    () => filterUsageProviders(snapshot?.providers ?? [], providerFilter, deferredSearch),
    [deferredSearch, providerFilter, snapshot?.providers]
  );

  const totals = snapshot?.tokens;
  const successRate = snapshot ? usageSuccessRate(snapshot.success, snapshot.requests) : null;

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <div className={styles.eyebrow}>{t('usage_monitor.eyebrow')}</div>
          <h1>{t('usage_monitor.title')}</h1>
          <p>
            {snapshot?.since
              ? t('usage_monitor.since', { time: formatDateTimeValue(snapshot.since) })
              : t('usage_monitor.description')}
          </p>
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void loadUsage()}
          disabled={!connected || loading}
        >
          <span className={styles.buttonContent}>
            <IconRefreshCw size={15} className={loading ? styles.spinning : undefined} />
            {t('common.refresh')}
          </span>
        </Button>
      </header>

      {!snapshot?.enabled && !loading && (
        <div className={styles.warning} role="status">
          <IconAlertTriangle size={18} />
          <div>
            <strong>{t('usage_monitor.disabled_title')}</strong>
            <span>{t('usage_monitor.disabled_description')}</span>
          </div>
        </div>
      )}

      {snapshot?.truncated && (
        <div className={styles.warning} role="status">
          <IconAlertTriangle size={18} />
          <div>
            <strong>{t('usage_monitor.truncated_title')}</strong>
            <span>{t('usage_monitor.truncated_description')}</span>
          </div>
        </div>
      )}

      {error && (
        <div className={styles.error} role="alert">
          {t('usage_monitor.load_failed', { message: error })}
        </div>
      )}

      <nav className={styles.tabs} aria-label={t('usage_monitor.tabs_label')}>
        {(['overview', 'keys', 'groups'] as UsageTab[]).map((id) => (
          <button
            key={id}
            type="button"
            className={`${styles.tab} ${tab === id ? styles.tabActive : ''}`}
            onClick={() => setTab(id)}
            aria-pressed={tab === id}
          >
            {t(`usage_monitor.tab_${id}`)}
          </button>
        ))}
      </nav>

      {tab === 'keys' && <KeyUsageTab connected={connected} />}
      {tab === 'groups' && <GroupUsageTab connected={connected} />}

      {tab === 'overview' && (
        <>
          <section className={styles.kpis} aria-label={t('usage_monitor.summary')}>
            {[
              ['total', t('usage_monitor.total_tokens'), totals?.totalTokens ?? 0],
              ['input', t('usage_monitor.input_tokens'), totals?.inputTokens ?? 0],
              ['output', t('usage_monitor.output_tokens'), totals?.outputTokens ?? 0],
              ['reasoning', t('usage_monitor.reasoning_tokens'), totals?.reasoningTokens ?? 0],
              ['cache-read', t('usage_monitor.cache_tokens'), totals?.cacheReadTokens ?? 0],
              [
                'cache-creation',
                t('usage_monitor.cache_creation_tokens'),
                totals?.cacheCreationTokens ?? 0,
              ],
              ['requests', t('usage_monitor.requests'), snapshot?.requests ?? 0],
            ].map(([key, label, value]) => (
              <article key={String(key)} className={styles.kpi}>
                <span>{label}</span>
                <strong>{formatTokenValue(Number(value))}</strong>
                {key === 'requests' && (
                  <small>
                    {successRate === null
                      ? t('usage_monitor.no_requests')
                      : t('usage_monitor.success_rate', { value: formatPercent(successRate) })}
                  </small>
                )}
              </article>
            ))}
          </section>

          <section className={styles.toolbar} aria-label={t('usage_monitor.filters')}>
            <div className={styles.providerFilter}>
              <Select
                value={providerFilter}
                options={providerOptions}
                onChange={setProviderFilter}
                ariaLabel={t('usage_monitor.provider_filter')}
                size="sm"
              />
            </div>
            <div className={styles.search}>
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t('usage_monitor.search_placeholder')}
                aria-label={t('usage_monitor.search_placeholder')}
                rightElement={<IconSearch size={16} />}
              />
            </div>
            <span className={styles.updated}>
              {snapshot?.updatedAt
                ? t('usage_monitor.updated_at', { time: formatDateTimeValue(snapshot.updatedAt) })
                : t('usage_monitor.waiting_for_usage')}
            </span>
          </section>

          {loading && !snapshot ? (
            <div className={styles.loading}>{t('common.loading')}</div>
          ) : visibleProviders.length === 0 ? (
            <EmptyState
              title={
                search || providerFilter !== 'all'
                  ? t('usage_monitor.no_matches')
                  : t('usage_monitor.empty_title')
              }
              description={
                search || providerFilter !== 'all'
                  ? t('usage_monitor.no_matches_description')
                  : t('usage_monitor.empty_description')
              }
            />
          ) : (
            <div className={styles.providers}>
              {visibleProviders.map((provider) => {
                const icon = getAuthFileIcon(provider.provider, resolvedTheme);
                const providerRate = usageSuccessRate(provider.success, provider.requests);
                return (
                  <section key={provider.provider} className={styles.providerCard}>
                    <header className={styles.providerHeader}>
                      <div className={styles.providerIdentity}>
                        <span className={styles.providerIcon}>
                          {icon ? (
                            <img src={icon} alt="" />
                          ) : (
                            getTypeLabel(t, provider.provider).slice(0, 1).toUpperCase()
                          )}
                        </span>
                        <div>
                          <h2>{getTypeLabel(t, provider.provider)}</h2>
                          <p>
                            {t('usage_monitor.provider_summary', {
                              models: provider.models.length,
                              requests: provider.requests,
                            })}
                          </p>
                        </div>
                      </div>
                      <div className={styles.providerStats}>
                        <span>
                          {t('usage_monitor.tokens_short')}
                          <strong>{formatTokenValue(provider.tokens.totalTokens)}</strong>
                        </span>
                        <span>
                          {t('usage_monitor.success_short')}
                          <strong>
                            {providerRate === null ? DASH : formatPercent(providerRate)}
                          </strong>
                        </span>
                      </div>
                    </header>

                    <div className={styles.tableScroll}>
                      <table>
                        <thead>
                          <tr>
                            <th>{t('usage_monitor.model')}</th>
                            <th>{t('usage_monitor.requests')}</th>
                            <th>{t('usage_monitor.input_tokens')}</th>
                            <th>{t('usage_monitor.output_tokens')}</th>
                            <th>{t('usage_monitor.reasoning_tokens')}</th>
                            <th>{t('usage_monitor.cache_tokens')}</th>
                            <th>{t('usage_monitor.cache_creation_tokens')}</th>
                            <th>{t('usage_monitor.total_tokens')}</th>
                            <th>{t('usage_monitor.last_used')}</th>
                          </tr>
                        </thead>
                        <tbody>
                          {provider.models.map((model) => (
                            <tr key={model.model}>
                              <td>
                                <code>{model.model}</code>
                                {model.failed > 0 && (
                                  <small>
                                    {t('usage_monitor.failed_requests', { count: model.failed })}
                                  </small>
                                )}
                              </td>
                              <td>{model.requests.toLocaleString()}</td>
                              <td>{model.tokens.inputTokens.toLocaleString()}</td>
                              <td>{model.tokens.outputTokens.toLocaleString()}</td>
                              <td>{model.tokens.reasoningTokens.toLocaleString()}</td>
                              <td>{model.tokens.cacheReadTokens.toLocaleString()}</td>
                              <td>{model.tokens.cacheCreationTokens.toLocaleString()}</td>
                              <td className={styles.totalCell}>
                                {model.tokens.totalTokens.toLocaleString()}
                              </td>
                              <td>{formatDateTimeValue(model.lastUsedAt) || DASH}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  </section>
                );
              })}
            </div>
          )}
        </>
      )}
    </div>
  );
}
