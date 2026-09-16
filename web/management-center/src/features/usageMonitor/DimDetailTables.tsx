import { useTranslation } from 'react-i18next';
import type { UsageDimRow } from '@/types';
import { formatCompactNumber, formatDateTimeValue } from '@/utils/format';
import styles from './UsageDimensions.module.scss';

const DASH = '—';

const formatTokens = (value: number): string =>
  value < 100_000 ? value.toLocaleString() : formatCompactNumber(value);

interface DimDetailTablesProps {
  models: UsageDimRow[];
  daily: UsageDimRow[];
  auths: UsageDimRow[];
}

const DimTable = ({ title, rows }: { title: string; rows: UsageDimRow[] }) => {
  const { t } = useTranslation();
  return (
    <div className={styles.dimBlock}>
      <h4>{title}</h4>
      {rows.length === 0 ? (
        <span className={styles.dimEmpty}>{t('usage_monitor.dim_empty')}</span>
      ) : (
        <table>
          <thead>
            <tr>
              <th />
              <th>{t('usage_monitor.total_tokens')}</th>
              <th>{t('usage_monitor.requests')}</th>
              <th>{t('usage_monitor.failed_short')}</th>
              <th>{t('usage_monitor.last_used')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.name}>
                <td>
                  <code>{row.name}</code>
                </td>
                <td>{formatTokens(row.tokens)}</td>
                <td>{row.requests.toLocaleString()}</td>
                <td className={row.failed > 0 ? styles.badCell : undefined}>{row.failed}</td>
                <td>{formatDateTimeValue(row.lastUsedAt) || DASH}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
};

export function DimDetailTables({ models, daily, auths }: DimDetailTablesProps) {
  const { t } = useTranslation();
  return (
    <div className={styles.dimGrid}>
      <DimTable title={t('usage_monitor.dim_models')} rows={models} />
      <DimTable title={t('usage_monitor.dim_daily')} rows={daily} />
      <DimTable title={t('usage_monitor.dim_auths')} rows={auths} />
    </div>
  );
}
