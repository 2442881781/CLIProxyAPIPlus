import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { IconRefreshCw } from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { modelPressureApi } from '@/services/api';
import { useAuthStore } from '@/stores';
import type { ModelPressureSnapshot } from '@/types';
import { formatPercent } from '@/utils/format';
import styles from './ModelPressurePage.module.scss';

const AUTO_REFRESH_MS = 5_000;
const DASH = '—';

const formatRate = (value: number): string => (value === 0 ? '0' : value.toFixed(2));

export function ModelPressurePage() {
  const { t } = useTranslation();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const [snapshot, setSnapshot] = useState<ModelPressureSnapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const loadingRef = useRef(false);

  const load = useCallback(async () => {
    if (!connected || loadingRef.current) return;
    loadingRef.current = true;
    setError('');
    try {
      setSnapshot(await modelPressureApi.getSnapshot());
    } catch (loadError: unknown) {
      setError(loadError instanceof Error ? loadError.message : t('common.unknown_error'));
    } finally {
      loadingRef.current = false;
      setLoading(false);
    }
  }, [connected, t]);

  useHeaderRefresh(load, connected);

  useEffect(() => {
    if (!connected) {
      setLoading(false);
      return;
    }
    void load();
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load();
    }, AUTO_REFRESH_MS);
    return () => window.clearInterval(timer);
  }, [connected, load]);

  const models = snapshot?.models ?? [];

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <div className={styles.eyebrow}>{t('model_pressure.eyebrow')}</div>
          <h1>{t('model_pressure.title')}</h1>
          <p>
            {t('model_pressure.description', { window: snapshot?.windowSeconds ?? 60 })}
          </p>
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void load()}
          disabled={!connected || loading}
        >
          <span className={styles.buttonContent}>
            <IconRefreshCw size={15} className={loading ? styles.spinning : undefined} />
            {t('common.refresh')}
          </span>
        </Button>
      </header>

      {error && (
        <div className={styles.error} role="alert">
          {t('model_pressure.load_failed', { message: error })}
        </div>
      )}

      {loading && !snapshot ? (
        <div className={styles.loading}>{t('common.loading')}</div>
      ) : models.length === 0 ? (
        <EmptyState
          title={t('model_pressure.empty_title')}
          description={t('model_pressure.empty_description')}
        />
      ) : (
        <div className={styles.tableCard}>
          <div className={styles.tableScroll}>
            <table>
              <thead>
                <tr>
                  <th>{t('model_pressure.model')}</th>
                  <th>{t('model_pressure.in_flight')}</th>
                  <th>{t('model_pressure.requests_per_second')}</th>
                  <th>{t('model_pressure.output_tps')}</th>
                  <th>{t('model_pressure.error_rate')}</th>
                  <th>{t('model_pressure.avg_latency')}</th>
                  <th>{t('model_pressure.avg_ttft')}</th>
                  <th>{t('model_pressure.supply')}</th>
                  <th>{t('model_pressure.providers')}</th>
                </tr>
              </thead>
              <tbody>
                {models.map((row) => {
                  const cooling = row.suspendedAuths + row.quotaExceededAuths;
                  return (
                    <tr key={row.model}>
                      <td>
                        <code>{row.model}</code>
                      </td>
                      <td className={row.inFlight > 0 ? styles.hotCell : undefined}>
                        {row.inFlight}
                      </td>
                      <td>{formatRate(row.requestsPerSecond)}</td>
                      <td className={styles.totalCell}>{formatRate(row.outputTokensPerSecond)}</td>
                      <td className={row.errorRate > 0 ? styles.badCell : undefined}>
                        {formatPercent(row.errorRate)}
                      </td>
                      <td>{row.avgLatencyMs > 0 ? `${row.avgLatencyMs} ms` : DASH}</td>
                      <td>{row.avgTtftMs > 0 ? `${row.avgTtftMs} ms` : DASH}</td>
                      <td>
                        {t('model_pressure.supply_value', {
                          serving: row.servingAuths,
                          cooling,
                        })}
                      </td>
                      <td className={styles.providersCell}>
                        {row.providers.length > 0 ? row.providers.join(', ') : DASH}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}
