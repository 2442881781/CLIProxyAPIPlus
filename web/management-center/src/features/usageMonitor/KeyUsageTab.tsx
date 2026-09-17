import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { IconDownload } from '@/components/ui/icons';
import { accessControlApi } from '@/services/api';
import type {
  AccessKeyUsageDetail,
  AccessKeyUsagePeriod,
  AccessKeyUsageTopBy,
  AccessKeyUsageTopRow,
} from '@/types';
import { formatCompactNumber, formatFileSize } from '@/utils/format';
import { DimDetailTables } from './DimDetailTables';
import styles from './UsageDimensions.module.scss';

const DASH = '—';
const PAGE_SIZE = 20;
const GIB = 1024 ** 3;
/** Shared with the access-keys page so the operator only enters the price once. */
const PRICE_STORAGE_KEY = 'akTrafficPrice';

const formatTokens = (value: number): string =>
  value < 100_000 ? value.toLocaleString() : formatCompactNumber(value);

const readPrice = (): number => {
  try {
    const stored = Number(localStorage.getItem(PRICE_STORAGE_KEY));
    return Number.isFinite(stored) && stored > 0 ? stored : 0;
  } catch {
    return 0;
  }
};

const csvCell = (value: string | number): string => {
  const text = String(value ?? '');
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
};

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
  const [price, setPrice] = useState<number>(() => readPrice());
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

  const cost = useCallback(
    (bytes: number): string => (price > 0 ? `¥${((bytes / GIB) * price).toFixed(2)}` : ''),
    [price]
  );

  const totals = useMemo(() => {
    let bytes = 0;
    let tokens = 0;
    rows.forEach((row) => {
      bytes += row.bytes;
      tokens += row.tokens;
    });
    return { bytes, tokens };
  }, [rows]);

  const updatePrice = (value: string) => {
    const parsed = Number(value);
    const next = Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
    setPrice(next);
    try {
      if (next > 0) {
        localStorage.setItem(PRICE_STORAGE_KEY, String(next));
      } else {
        localStorage.removeItem(PRICE_STORAGE_KEY);
      }
    } catch {
      // Storage can be unavailable (private mode); the price still applies.
    }
  };

  const exportCsv = () => {
    const header = [
      'name',
      'key_prefix',
      'group',
      'tokens',
      'requests',
      'failed',
      'bytes_in',
      'bytes_out',
      'bytes',
      ...(price > 0 ? ['cost_cny'] : []),
    ];
    const lines = rows.map((row) =>
      [
        row.name,
        row.keyPrefix,
        row.group,
        row.tokens,
        row.requests,
        row.failed,
        row.bytesIn,
        row.bytesOut,
        row.bytes,
        ...(price > 0 ? [(row.bytes / GIB) * price] : []),
      ]
        .map(csvCell)
        .join(',')
    );
    const blob = new Blob([`\uFEFF${[header.join(','), ...lines].join('\r\n')}`], {
      type: 'text/csv;charset=utf-8',
    });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = `access-key-usage-${by}-${period}.csv`;
    anchor.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
  };

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
            { value: 'bytes', label: t('usage_monitor.by_bytes') },
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
        <div className={styles.toolbarField}>
          <Input
            type="number"
            min={0}
            step={0.01}
            value={price > 0 ? String(price) : ''}
            onChange={(event) => updatePrice(event.target.value)}
            placeholder={t('usage_monitor.price_placeholder')}
            aria-label={t('usage_monitor.price_label')}
          />
        </div>
        <Button variant="secondary" size="sm" onClick={exportCsv} disabled={rows.length === 0}>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
            <IconDownload size={15} />
            {t('usage_monitor.export_csv')}
          </span>
        </Button>
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
                  <th>{t('usage_monitor.traffic')}</th>
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
                      <td className={styles.totalCell}>
                        {formatFileSize(row.bytes)}
                        {price > 0 && (
                          <div className={styles.costCell}>{cost(row.bytes)}</div>
                        )}
                      </td>
                    </tr>
                    {expandedId === row.id && (
                      <tr className={styles.detailRow}>
                        <td colSpan={8}>
                          {detailLoading ? (
                            <span className={styles.dimEmpty}>{t('common.loading')}</span>
                          ) : detail ? (
                            <>
                              {detail.traffic && (
                                <div className={styles.dimEmpty}>
                                  {t('usage_monitor.traffic_detail', {
                                    client: formatFileSize(detail.traffic.clientTotal),
                                    upstream: formatFileSize(detail.traffic.upstreamTotal),
                                    period: formatFileSize(detail.traffic.periodTotal),
                                  })}
                                </div>
                              )}
                              <DimDetailTables
                                models={detail.models}
                                daily={detail.daily}
                                auths={detail.auths}
                              />
                            </>
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

      {!loading && rows.length > 0 && (
        <div className={styles.dimEmpty}>
          {t('usage_monitor.traffic_total', {
            tokens: formatTokens(totals.tokens),
            bytes: formatFileSize(totals.bytes),
          })}
          {price > 0 ? ` · ${cost(totals.bytes)}` : ''}
        </div>
      )}
    </section>
  );
}
