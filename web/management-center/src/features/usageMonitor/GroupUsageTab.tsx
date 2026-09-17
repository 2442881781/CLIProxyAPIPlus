import { Fragment, useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { accessControlApi } from '@/services/api';
import type { AccessGroup, AccessGroupUsageDetail } from '@/types';
import { formatCompactNumber, formatDateTimeValue, formatFileSize } from '@/utils/format';
import { DimDetailTables } from './DimDetailTables';
import styles from './UsageDimensions.module.scss';

const DASH = '—';

const formatTokens = (value: number): string =>
  value < 100_000 ? value.toLocaleString() : formatCompactNumber(value);

export function GroupUsageTab({ connected }: { connected: boolean }) {
  const { t } = useTranslation();
  const [groups, setGroups] = useState<AccessGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [expandedName, setExpandedName] = useState<string | null>(null);
  const [detail, setDetail] = useState<AccessGroupUsageDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const detailCacheRef = useRef(new Map<string, AccessGroupUsageDetail>());

  const load = useCallback(async () => {
    if (!connected) return;
    setError('');
    try {
      setGroups(await accessControlApi.listGroups());
    } catch (loadError: unknown) {
      setError(loadError instanceof Error ? loadError.message : t('common.unknown_error'));
    } finally {
      setLoading(false);
    }
  }, [connected, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const toggleRow = async (name: string) => {
    if (expandedName === name) {
      setExpandedName(null);
      setDetail(null);
      return;
    }
    setExpandedName(name);
    const cached = detailCacheRef.current.get(name);
    if (cached) {
      setDetail(cached);
      return;
    }
    setDetailLoading(true);
    setDetail(null);
    try {
      const loaded = await accessControlApi.getGroupUsage(name);
      detailCacheRef.current.set(name, loaded);
      setDetail(loaded);
    } catch {
      setDetail(null);
    } finally {
      setDetailLoading(false);
    }
  };

  return (
    <section className={styles.tabBody}>
      {error && (
        <div className={styles.error} role="alert">
          {error}
        </div>
      )}

      {loading ? (
        <div className={styles.loading}>{t('common.loading')}</div>
      ) : groups.length === 0 ? (
        <div className={styles.loading}>{t('usage_monitor.no_group_usage')}</div>
      ) : (
        <div className={styles.tableCard}>
          <div className={styles.tableScroll}>
            <table>
              <thead>
                <tr>
                  <th>{t('access_groups.col_name')}</th>
                  <th>{t('usage_monitor.group_models')}</th>
                  <th>{t('usage_monitor.total_tokens')}</th>
                  <th>{t('usage_monitor.traffic')}</th>
                  <th>{t('usage_monitor.requests')}</th>
                  <th>{t('usage_monitor.failed_short')}</th>
                  <th>{t('usage_monitor.last_used')}</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((group) => (
                  <Fragment key={group.name}>
                    <tr className={styles.clickableRow} onClick={() => void toggleRow(group.name)}>
                      <td>
                        <code>{group.name}</code>
                      </td>
                      <td>
                        {group.allowedModels.length
                          ? group.allowedModels.join(', ')
                          : t('access_groups.all_models')}
                      </td>
                      <td className={styles.totalCell}>{formatTokens(group.usage.tokens)}</td>
                      <td className={styles.totalCell}>{formatFileSize(group.usage.bytes)}</td>
                      <td>{group.usage.requests.toLocaleString()}</td>
                      <td className={group.usage.failed > 0 ? styles.badCell : undefined}>
                        {group.usage.failed}
                      </td>
                      <td>{formatDateTimeValue(group.usage.lastUsedAt) || DASH}</td>
                    </tr>
                    {expandedName === group.name && (
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
