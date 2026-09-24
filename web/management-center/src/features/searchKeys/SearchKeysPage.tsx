import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Modal } from '@/components/ui/Modal';
import { IconPlus, IconRefreshCw } from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { searchKeysApi, type SearchKeyEntry, type SearchKeyStatus } from '@/services/api';
import { useAuthStore, useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import { formatDateTimeValue, formatDateValue } from '@/utils/format';
import { SearchKeyEditSheet } from './SearchKeyEditSheet';
import { SearchMcpSettingsCard } from './SearchMcpSettingsCard';
import {
  buildSearchKeyRows,
  buildSearchUsageSnippets,
  describeQuota,
  formatCooldown,
  formatQuotaAmount,
  summarizeSearchProviders,
  type SearchKeyRow,
} from './searchKeysModel';
import styles from './SearchKeysPage.module.scss';

const DASH = '—';
const STATUS_POLL_MS = 15_000;

const REMOTE_QUOTA_PROVIDERS = new Set(['tavily', 'firecrawl']);

const maskKey = (key: string): string =>
  key.length <= 8 ? '****' : `${key.slice(0, 4)}****${key.slice(-4)}`;

export function SearchKeysPage() {
  const { t } = useTranslation();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const apiBase = useAuthStore((state) => state.apiBase);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [entries, setEntries] = useState<SearchKeyEntry[]>([]);
  const [statuses, setStatuses] = useState<SearchKeyStatus[]>([]);
  const [now, setNow] = useState(() => Date.now());
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [sheet, setSheet] = useState<{ open: boolean; index: number | null }>({
    open: false,
    index: null,
  });
  const [deleting, setDeleting] = useState<SearchKeyRow | null>(null);
  const [mutating, setMutating] = useState(false);
  const loadingRef = useRef(false);

  const load = useCallback(async () => {
    if (!connected || loadingRef.current) return;
    loadingRef.current = true;
    setError('');
    try {
      const [entryRows, statusRows] = await Promise.all([
        searchKeysApi.list(),
        searchKeysApi.status(),
      ]);
      setEntries(entryRows);
      setStatuses(statusRows);
      setNow(Date.now());
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
    const timer = window.setInterval(() => void load(), STATUS_POLL_MS);
    return () => window.clearInterval(timer);
  }, [connected, load]);

  const rows = useMemo(() => buildSearchKeyRows(entries, statuses, now), [entries, statuses, now]);
  const summary = useMemo(() => summarizeSearchProviders(rows), [rows]);
  const snippets = useMemo(() => buildSearchUsageSnippets(apiBase), [apiBase]);

  const mutate = useCallback(
    async (action: () => Promise<unknown>, successMessage?: string) => {
      setMutating(true);
      setError('');
      try {
        await action();
        if (successMessage) showNotification(successMessage, 'success');
        await load();
        return true;
      } catch (mutateError: unknown) {
        setError(mutateError instanceof Error ? mutateError.message : t('common.unknown_error'));
        return false;
      } finally {
        setMutating(false);
      }
    },
    [load, showNotification, t]
  );

  const handleCopy = useCallback(
    async (text: string) => {
      if (await copyToClipboard(text)) showNotification(t('search_keys.copied'), 'success');
    },
    [showNotification, t]
  );

  const refreshQuota = (target: { provider: string; apiKey: string } | 'all') =>
    void mutate(async () => {
      setStatuses(await searchKeysApi.refreshQuota(target));
    }, t('search_keys.quota_refreshed'));

  const stateLabel = (row: SearchKeyRow): string => {
    if (row.state === 'disabled') return t('search_keys.state_disabled');
    if (row.state === 'exhausted') return t('search_keys.state_exhausted');
    if (row.state === 'cooling') {
      return t('search_keys.state_cooling', { time: formatCooldown(row.cooldownRemainingMs) });
    }
    return t('search_keys.state_active');
  };

  const editingEntry = sheet.index === null ? null : (entries[sheet.index] ?? null);

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <div className={styles.eyebrow}>{t('search_keys.eyebrow')}</div>
          <h1>{t('search_keys.title')}</h1>
          <p>{t('search_keys.description')}</p>
        </div>
        <div className={styles.headerActions}>
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
          <Button
            variant="secondary"
            size="sm"
            onClick={() => refreshQuota('all')}
            disabled={!connected || mutating || rows.length === 0}
          >
            {t('search_keys.refresh_quota')}
          </Button>
          {rows.some((row) => row.state === 'cooling') ? (
            <Button
              variant="secondary"
              size="sm"
              disabled={mutating}
              onClick={() =>
                void mutate(async () => {
                  await searchKeysApi.resetCooldown('all');
                })
              }
            >
              {t('search_keys.reset_all')}
            </Button>
          ) : null}
          <Button
            size="sm"
            onClick={() => setSheet({ open: true, index: null })}
            disabled={!connected}
          >
            <span className={styles.buttonContent}>
              <IconPlus size={15} />
              {t('search_keys.add')}
            </span>
          </Button>
        </div>
      </header>

      {error && (
        <div className={styles.error} role="alert">
          {error}
        </div>
      )}

      <section className={styles.summaryGrid}>
        {summary.map((item) => (
          <div key={item.provider} className={styles.summaryCard}>
            <div className={styles.summaryProvider}>{item.provider}</div>
            <div className={styles.summaryValue}>
              {item.total
                ? t('search_keys.summary_available', { active: item.active, total: item.total })
                : t('search_keys.summary_none')}
            </div>
            {item.remaining !== null && (
              <div className={styles.summaryRemaining}>
                {t('search_keys.summary_remaining', {
                  amount: formatQuotaAmount(item.remaining, item.unit),
                })}
              </div>
            )}
            <div className={styles.summaryMeta}>
              {item.exhausted > 0 && (
                <span className={styles.state_exhausted}>
                  {t('search_keys.summary_exhausted', { count: item.exhausted })}
                </span>
              )}
              {item.cooling > 0 && (
                <span className={styles.badgeCooling}>
                  {t('search_keys.summary_cooling', { count: item.cooling })}
                </span>
              )}
              {item.disabled > 0 && (
                <span className={styles.badgeDisabled}>
                  {t('search_keys.summary_disabled', { count: item.disabled })}
                </span>
              )}
            </div>
          </div>
        ))}
      </section>

      {loading && rows.length === 0 ? (
        <div className={styles.loading}>{t('common.loading')}</div>
      ) : rows.length === 0 ? (
        <EmptyState
          title={t('search_keys.empty_title')}
          description={t('search_keys.empty_description')}
        />
      ) : (
        <div className={styles.tableCard}>
          <div className={styles.tableScroll}>
            <table>
              <thead>
                <tr>
                  <th>{t('search_keys.col_provider')}</th>
                  <th>{t('search_keys.col_label')}</th>
                  <th>{t('search_keys.col_key')}</th>
                  <th>{t('search_keys.col_state')}</th>
                  <th>{t('search_keys.col_quota')}</th>
                  <th>{t('search_keys.col_usage')}</th>
                  <th>{t('search_keys.col_last_status')}</th>
                  <th>{t('search_keys.col_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={`${row.entry.provider}:${row.index}`}>
                    <td>
                      <code>{row.entry.provider}</code>
                    </td>
                    <td>{row.entry.label || DASH}</td>
                    <td>
                      <code>{row.status?.maskedKey || maskKey(row.entry.apiKey)}</code>
                    </td>
                    <td>
                      <span className={styles[`state_${row.state}`]}>{stateLabel(row)}</span>
                    </td>
                    <td>
                      <QuotaCell row={row} />
                    </td>
                    <td>
                      {row.status
                        ? t('search_keys.usage_sub', {
                            requests: row.status.requests,
                            failures: row.status.failures,
                          })
                        : DASH}
                    </td>
                    <td>{row.status?.lastStatus || DASH}</td>
                    <td>
                      <div className={styles.rowActions}>
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => setSheet({ open: true, index: row.index })}
                        >
                          {t('common.edit')}
                        </Button>
                        <Button
                          variant="secondary"
                          size="sm"
                          disabled={mutating}
                          onClick={() =>
                            void mutate(() =>
                              searchKeysApi.update(row.index, {
                                ...row.entry,
                                disabled: !row.entry.disabled,
                              })
                            )
                          }
                        >
                          {row.entry.disabled ? t('search_keys.enable') : t('search_keys.disable')}
                        </Button>
                        {REMOTE_QUOTA_PROVIDERS.has(row.entry.provider) ? (
                          <Button
                            variant="secondary"
                            size="sm"
                            disabled={mutating}
                            onClick={() =>
                              refreshQuota({
                                provider: row.entry.provider,
                                apiKey: row.entry.apiKey,
                              })
                            }
                          >
                            {t('search_keys.refresh_quota')}
                          </Button>
                        ) : null}
                        {row.entry.provider === 'exa' && (row.status?.quota?.used ?? 0) > 0 ? (
                          <Button
                            variant="secondary"
                            size="sm"
                            disabled={mutating}
                            onClick={() =>
                              void mutate(
                                () =>
                                  searchKeysApi.resetSpend({
                                    provider: row.entry.provider,
                                    apiKey: row.entry.apiKey,
                                  }),
                                t('search_keys.spend_reset')
                              )
                            }
                          >
                            {t('search_keys.reset_spend')}
                          </Button>
                        ) : null}
                        {row.state === 'cooling' ? (
                          <Button
                            variant="secondary"
                            size="sm"
                            disabled={mutating}
                            onClick={() =>
                              void mutate(() =>
                                searchKeysApi.resetCooldown({
                                  provider: row.entry.provider,
                                  apiKey: row.entry.apiKey,
                                })
                              )
                            }
                          >
                            {t('search_keys.reset_cooldown')}
                          </Button>
                        ) : null}
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => setDeleting(row)}
                          disabled={mutating}
                        >
                          {t('common.delete')}
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <SearchMcpSettingsCard connected={connected} />

      <section className={styles.usageCard}>
        <h2>{t('search_keys.usage_title')}</h2>
        <p className={styles.hint}>{t('search_keys.usage_description')}</p>

        <div className={styles.usageBlock}>
          <div className={styles.label}>{t('search_keys.usage_rest')}</div>
          {Object.entries(snippets.restBase).map(([provider, url]) => (
            <SnippetLine key={provider} label={provider} value={url} onCopy={handleCopy} />
          ))}
          <span className={styles.hint}>{t('search_keys.usage_rest_hint')}</span>
        </div>

        <div className={styles.usageBlock}>
          <div className={styles.label}>{t('search_keys.usage_mcp')}</div>
          <SnippetLine label="URL" value={snippets.mcpUrl} onCopy={handleCopy} />
          <SnippetLine label="Claude Code" value={snippets.claudeMcpCommand} onCopy={handleCopy} />
          <span className={styles.hint}>{t('search_keys.usage_mcp_hint')}</span>
        </div>

        <div className={styles.usageBlock}>
          <div className={styles.label}>{t('search_keys.usage_firecrawl_mcp')}</div>
          <SnippetLine label="env" value={snippets.firecrawlMcpEnv} onCopy={handleCopy} />
          <span className={styles.hint}>{t('search_keys.usage_firecrawl_mcp_hint')}</span>
        </div>
      </section>

      <SearchKeyEditSheet
        open={sheet.open}
        entry={editingEntry}
        editingIndex={sheet.index ?? undefined}
        existing={entries}
        mutating={mutating}
        onClose={() => setSheet({ open: false, index: null })}
        onSubmit={async (entry) => {
          const saved = await mutate(
            () =>
              sheet.index === null
                ? searchKeysApi.replace([...entries, entry])
                : searchKeysApi.update(sheet.index, entry),
            t('search_keys.saved')
          );
          if (saved) setSheet({ open: false, index: null });
        }}
      />

      <Modal
        open={deleting !== null}
        title={t('search_keys.delete_title', { provider: deleting?.entry.provider ?? '' })}
        onClose={() => setDeleting(null)}
        closeDisabled={mutating}
        footer={
          <>
            <Button variant="secondary" onClick={() => setDeleting(null)} disabled={mutating}>
              {t('common.cancel')}
            </Button>
            <Button
              onClick={() => {
                if (!deleting) return;
                void mutate(() => searchKeysApi.remove(deleting.index)).then((ok) => {
                  if (ok) setDeleting(null);
                });
              }}
              disabled={mutating}
            >
              {mutating ? t('common.loading') : t('common.delete')}
            </Button>
          </>
        }
      >
        <p className={styles.hint}>{t('search_keys.delete_hint')}</p>
      </Modal>
    </div>
  );
}

function QuotaCell({ row }: { row: SearchKeyRow }) {
  const { t, i18n } = useTranslation();
  const quota = row.status?.quota ?? null;
  const display = describeQuota(quota);
  const meta: string[] = [];
  if (quota?.plan) meta.push(quota.plan);
  if (quota?.checkedAt) {
    meta.push(
      t('search_keys.quota_checked', { time: formatDateTimeValue(quota.checkedAt, i18n.language) })
    );
  }
  if (quota?.resetAt) {
    meta.push(
      t('search_keys.quota_reset_at', { date: formatDateValue(quota.resetAt, i18n.language) })
    );
  }
  return (
    <div className={styles.quotaCell}>
      {display ? (
        <span className={styles.quotaText}>
          {display.spentOnly
            ? t('search_keys.quota_spent', { amount: display.text })
            : display.text}
        </span>
      ) : (
        <span className={styles.quotaMuted}>{t('search_keys.quota_unknown')}</span>
      )}
      {display?.percentRemaining !== null && display?.percentRemaining !== undefined ? (
        <span className={styles.quotaBar} aria-hidden="true">
          <span style={{ width: `${Math.min(100, Math.max(0, display.percentRemaining))}%` }} />
        </span>
      ) : null}
      {meta.length ? <span className={styles.quotaMeta}>{meta.join(' · ')}</span> : null}
      {quota?.error ? (
        <span className={styles.quotaError} title={quota.error}>
          {t('search_keys.quota_error', { error: quota.error })}
        </span>
      ) : null}
    </div>
  );
}

function SnippetLine({
  label,
  value,
  onCopy,
}: {
  label: string;
  value: string;
  onCopy: (value: string) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.snippet}>
      <span className={styles.snippetLabel}>{label}</span>
      <code>{value}</code>
      <button type="button" className={styles.copyButton} onClick={() => void onCopy(value)}>
        {t('common.copy')}
      </button>
    </div>
  );
}
