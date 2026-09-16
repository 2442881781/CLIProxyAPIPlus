import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconRefreshCw } from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { serverStatsApi } from '@/services/api';
import { useAuthStore } from '@/stores';
import type { ServerStats } from '@/types';
import { formatFileSize, formatPercent } from '@/utils/format';
import styles from './ServerMonitorPage.module.scss';

const AUTO_REFRESH_MS = 5_000;
const DASH = '—';

const formatUptime = (seconds: number): string => {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m ${Math.floor(seconds % 60)}s`;
};

const Gauge = ({ percent, label }: { percent?: number; label: string }) => {
  const value = percent ?? 0;
  const level = value >= 90 ? styles.danger : value >= 70 ? styles.warn : '';
  return (
    <div className={styles.gauge}>
      <div className={styles.gaugeHead}>
        <span>{label}</span>
        <strong className={level}>{percent === undefined ? DASH : formatPercent(value)}</strong>
      </div>
      <div className={styles.gaugeTrack}>
        <div
          className={`${styles.gaugeFill} ${level}`}
          style={{ width: `${Math.min(100, Math.max(0, value))}%` }}
        />
      </div>
    </div>
  );
};

export function ServerMonitorPage() {
  const { t } = useTranslation();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const [stats, setStats] = useState<ServerStats | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const loadingRef = useRef(false);

  const load = useCallback(async () => {
    if (!connected || loadingRef.current) return;
    loadingRef.current = true;
    setError('');
    try {
      setStats(await serverStatsApi.getStats());
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

  const host = stats?.host;
  const memUsedPercent =
    host && host.memTotalBytes > 0
      ? ((host.memTotalBytes - host.memAvailableBytes) / host.memTotalBytes) * 100
      : undefined;

  const processCards: Array<[string, string, string?]> = stats
    ? [
        ['active_requests', String(stats.activeRequests), t('server_monitor.active_requests_hint')],
        ['active_websockets', String(stats.activeWebsockets)],
        ['goroutines', stats.goroutines.toLocaleString()],
        ['heap', formatFileSize(stats.heapAllocBytes), `sys ${formatFileSize(stats.heapSysBytes)}`],
        ['stack', formatFileSize(stats.stackInuseBytes)],
        ['gc_count', stats.gcCount.toLocaleString()],
        ['num_fds', stats.numFds === undefined ? DASH : stats.numFds.toLocaleString()],
        ['uptime', formatUptime(stats.uptimeSeconds)],
        [
          'cpu_seconds',
          stats.processCpuSeconds === undefined ? DASH : `${stats.processCpuSeconds.toFixed(1)}s`,
        ],
      ]
    : [];

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <div className={styles.eyebrow}>{t('server_monitor.eyebrow')}</div>
          <h1>{t('server_monitor.title')}</h1>
          <p>{t('server_monitor.description')}</p>
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
          {t('server_monitor.load_failed', { message: error })}
        </div>
      )}

      {loading && !stats ? (
        <div className={styles.loading}>{t('common.loading')}</div>
      ) : stats ? (
        <>
          <section className={styles.section} aria-label={t('server_monitor.gauges')}>
            <Gauge percent={stats.processCpuPercent} label={t('server_monitor.process_cpu')} />
            <Gauge percent={host?.cpuPercent} label={t('server_monitor.host_cpu')} />
            <Gauge percent={memUsedPercent} label={t('server_monitor.host_memory')} />
          </section>

          <section aria-label={t('server_monitor.process_section')}>
            <h2 className={styles.sectionTitle}>{t('server_monitor.process_section')}</h2>
            <div className={styles.kpis}>
              {processCards.map(([key, value, hint]) => (
                <article key={key} className={styles.kpi}>
                  <span>{t(`server_monitor.${key}`)}</span>
                  <strong>{value}</strong>
                  {hint ? <small>{hint}</small> : null}
                </article>
              ))}
            </div>
          </section>

          {host ? (
            <section aria-label={t('server_monitor.host_section')}>
              <h2 className={styles.sectionTitle}>{t('server_monitor.host_section')}</h2>
              <div className={styles.kpis}>
                <article className={styles.kpi}>
                  <span>{t('server_monitor.load')}</span>
                  <strong>
                    {host.load1.toFixed(2)} / {host.load5.toFixed(2)} / {host.load15.toFixed(2)}
                  </strong>
                  <small>1 / 5 / 15 min</small>
                </article>
                <article className={styles.kpi}>
                  <span>{t('server_monitor.host_mem_detail')}</span>
                  <strong>{formatFileSize(host.memTotalBytes - host.memAvailableBytes)}</strong>
                  <small>
                    {t('server_monitor.host_mem_of', {
                      total: formatFileSize(host.memTotalBytes),
                    })}
                  </small>
                </article>
                <article className={styles.kpi}>
                  <span>{t('server_monitor.num_cpu')}</span>
                  <strong>{host.numCpu}</strong>
                </article>
              </div>
            </section>
          ) : (
            <div className={styles.notice} role="status">
              {t('server_monitor.host_unavailable')}
            </div>
          )}
        </>
      ) : null}
    </div>
  );
}
