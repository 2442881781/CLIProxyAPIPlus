import { Fragment, useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Select } from '@/components/ui/Select';
import { accessControlApi } from '@/services/api';
import type {
  AccessKeyUsageDetail,
  AccessKeyUsagePeriod,
  AccessKeyUsageTopBy,
  AccessKeyUsageTopRow,
} from '@/types';
import { formatCompactNumber } from '@/utils/format';
import { DimDetailTables } from './DimDetailTables';
import styles from './UsageDimensions.module.scss';

const DASH = '—';
const PAGE_SIZE = 20;

const formatTokens = (value: number): string =>
  value < 100_000 ? value.toLocaleString() : formatCompactNumber(value);

export function KeyUsageTab({ connected }: { connected: boolean }) {
  const { t } = useTranslation();
  const [by, setBy] = useState<AccessKeyUsageTopBy>('tokens');
  const [period, setPeriod] = useState<AccessKeyUsagePeriod>('all');
  const [rows, setRows] = useState<AccessKeyUsageTopRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [detail, setDetail] = useState<AccessKeyUsageDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const detailCacheRef = useRef(new Map<string, AccessKeyUsageDetail>());

  const load = useCallback(async () => {
    if (!connected) return;
    setError('');
    try {
      setRows(await accessControlApi.getKeyUsageTop(by, period, PAGE_SIZE));
    } catch (loadError: unknown) {
      setError(loadError instanceof Error ? loadError.message : t('common.unknown_error'));
    } finally {
      setLoading(false);
    }
  }, [by, period, connected, t]);

  useEffect(() => {
    setLoading(true);
    detailCacheRef.current.clear();
    void load();
  }, [load]);

  const toggleRow = async (id: string) => {
    if (expandedId === id) {
      setExpandedId(null);
      setDetail(null);
      return;
    }
    setExpandedId(id);
    const cached = detailCacheRef.current.get(id);
    if (cached) {
      setDetail(cached);
      return;
    }
    setDetailLoading(true);
    setDetail(null);
    try {
      const loaded = await accessControlApi.getKeyUsageDetail(id);
      detailCacheRef.current.set(id, loaded);
      setDetail(loaded);
    } catch {
      setDetail(null);
    } finally {
      setDetailLoading(false);
    }
  };

  return (
    <section className={styles.tabBody}>
      <div className={styles.toolbar}>
        <Select
          value={by}
          onChange={(value) => setBy(value as AccessKeyUsageTopBy)}
          options={[
            { value: 'tokens', label: t('usage_monitor.by_tokens') },
            { value: 'requests', label: t('usage_monitor.by_requests') },
            { value: 'failed', label: t('usage_monitor.by_failed') },
          ]}
          ariaLabel={t('usage_monitor.rank_by')}
          size="sm"
        />
        <Select
          value={period}
          onChange={(value) => setPeriod(value as AccessKeyUsagePeriod)}
          options={[
            { value: 'all', label: t('usage_monitor.period_all') },
            { value: 'month', label: t('usage_monitor.period_month') },
            { value: 'day', label: t('usage_monitor.period_day') },
          ]}
          ariaLabel={t('usage_monitor.rank_period')}
          size="sm"
        />
      </div>

      {error && (
        <div className={styles.error} role="alert">
          {error}
        </div>
      )}

      {loading ? (
        <div className={styles.loading}>{t('common.loading')}</div>
      ) : rows.length === 0 ? (
        <div className={styles.loading}>{t('usage_monitor.no_key_usage')}</div>
      ) : (
        <div className={styles.tableCard}>
          <div className={styles.tableScroll}>
            <table>
              <thead>
                <tr>
                  <th>#</th>
                  <th>{t('usage_monitor.key_name')}</th>
                  <th>{t('usage_monitor.key_prefix')}</th>
                  <th>{t('access_groups.col_name')}</th>
                  <th>{t('usage_monitor.total_tokens')}</th>
                  <th>{t('usage_monitor.requests')}</th>
                  <th>{t('usage_monitor.failed_short')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row, index) => (
                  <Fragment key={row.id}>
                    <tr className={styles.clickableRow} onClick={() => void toggleRow(row.id)}>
                      <td>{index + 1}</td>
                      <td>{row.name || DASH}</td>
                      <td>
                        <code>{row.keyPrefix}</code>
                      </td>
                      <td>{row.group || DASH}</td>
                      <td className={styles.totalCell}>{formatTokens(row.tokens)}</td>
                      <td>{row.requests.toLocaleString()}</td>
                      <td className={row.failed > 0 ? styles.badCell : undefined}>{row.failed}</td>
                    </tr>
                    {expandedId === row.id && (
                      <tr className={styles.detailRow}>
                        <td colSpan={7}>
                          {detailLoading ? (
                            <span className={styles.dimEmpty}>{t('common.loading')}</span>
                          ) : detail ? (
                            <DimDetailTables
                              models={detail.models}
                              daily={detail.daily}
                              auths={detail.auths}
                            />
                          ) : (
                            <span className={styles.dimEmpty}>{DASH}</span>
                          )}
                        </td>
                      </tr>
                    )}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </section>
  );
}
