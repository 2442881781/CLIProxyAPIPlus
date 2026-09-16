import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Modal } from '@/components/ui/Modal';
import { IconPlus, IconRefreshCw } from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useApiKeysForModels } from '@/hooks/useApiKeysForModels';
import { accessControlApi } from '@/services/api';
import { useAuthStore, useModelsStore } from '@/stores';
import type { AccessAuthItem, AccessGroup } from '@/types';
import { formatCompactNumber, formatDateTimeValue } from '@/utils/format';
import { GroupEditSheet } from './GroupEditSheet';
import styles from './AccessGroupsPage.module.scss';

const DASH = '—';

const formatTokens = (value: number): string =>
  value < 100_000 ? value.toLocaleString() : formatCompactNumber(value);

const summarizeLimits = (group: AccessGroup): string => {
  const parts: string[] = [];
  if (group.maxConcurrency > 0) parts.push(`C${group.maxConcurrency}`);
  if (group.rateLimitRpm > 0) parts.push(`${group.rateLimitRpm}rpm`);
  const pk = group.perKeyLimits;
  const perKey = [
    pk.rpm > 0 ? `${pk.rpm}rpm` : '',
    pk.tpm > 0 ? `${formatCompactNumber(pk.tpm)}tpm` : '',
    pk.rpd > 0 ? `${pk.rpd}rpd` : '',
    pk.maxConcurrency > 0 ? `C${pk.maxConcurrency}` : '',
  ]
    .filter(Boolean)
    .join('/');
  if (perKey) parts.push(`key:${perKey}`);
  return parts.length ? parts.join(' · ') : DASH;
};

export function AccessGroupsPage() {
  const { t } = useTranslation();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const apiBase = useAuthStore((state) => state.apiBase);
  const models = useModelsStore((state) => state.models);
  const modelsLoading = useModelsStore((state) => state.loading);
  const fetchModels = useModelsStore((state) => state.fetchModels);
  const resolveApiKeysForModels = useApiKeysForModels();
  const [groups, setGroups] = useState<AccessGroup[]>([]);
  const [auths, setAuths] = useState<AccessAuthItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [sheet, setSheet] = useState<{ open: boolean; group: AccessGroup | null }>({
    open: false,
    group: null,
  });
  const [deleting, setDeleting] = useState<AccessGroup | null>(null);
  const [mutating, setMutating] = useState(false);
  const [modelsError, setModelsError] = useState('');
  const loadingRef = useRef(false);

  const load = useCallback(async () => {
    if (!connected || loadingRef.current) return;
    loadingRef.current = true;
    setError('');
    try {
      const [groupRows, authRows] = await Promise.all([
        accessControlApi.listGroups(),
        accessControlApi.listAuths(),
      ]);
      setGroups(groupRows);
      setAuths(authRows);
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
  }, [connected, load]);

  useEffect(() => {
    if (!sheet.open) {
      setModelsError('');
      return;
    }
    if (!connected || !apiBase || models.length > 0 || modelsLoading || modelsError) return;
    let cancelled = false;
    setModelsError('');
    void (async () => {
      try {
        const apiKeys = await resolveApiKeysForModels();
        await fetchModels(apiBase, apiKeys[0]);
      } catch (modelError: unknown) {
        if (!cancelled) {
          setModelsError(modelError instanceof Error ? modelError.message : 'load failed');
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [
    apiBase,
    connected,
    fetchModels,
    models.length,
    modelsError,
    modelsLoading,
    resolveApiKeysForModels,
    sheet.open,
  ]);

  const handleDelete = useCallback(async () => {
    if (!deleting) return;
    setMutating(true);
    try {
      await accessControlApi.deleteGroup(deleting.name);
      setDeleting(null);
      await load();
    } catch (deleteError: unknown) {
      setError(deleteError instanceof Error ? deleteError.message : t('common.unknown_error'));
    } finally {
      setMutating(false);
    }
  }, [deleting, load, t]);

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <div className={styles.eyebrow}>{t('access_groups.eyebrow')}</div>
          <h1>{t('access_groups.title')}</h1>
          <p>{t('access_groups.description')}</p>
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
            size="sm"
            onClick={() => setSheet({ open: true, group: null })}
            disabled={!connected}
          >
            <span className={styles.buttonContent}>
              <IconPlus size={15} />
              {t('access_groups.create')}
            </span>
          </Button>
        </div>
      </header>

      {error && (
        <div className={styles.error} role="alert">
          {error}
        </div>
      )}

      {loading && groups.length === 0 ? (
        <div className={styles.loading}>{t('common.loading')}</div>
      ) : groups.length === 0 ? (
        <EmptyState
          title={t('access_groups.empty_title')}
          description={t('access_groups.empty_description')}
        />
      ) : (
        <div className={styles.tableCard}>
          <div className={styles.tableScroll}>
            <table>
              <thead>
                <tr>
                  <th>{t('access_groups.col_name')}</th>
                  <th>{t('access_groups.col_models')}</th>
                  <th>{t('access_groups.col_pool')}</th>
                  <th>{t('access_groups.col_limits')}</th>
                  <th>{t('access_groups.col_usage')}</th>
                  <th>{t('access_groups.col_updated')}</th>
                  <th>{t('access_groups.col_actions')}</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((group) => (
                  <tr key={group.name}>
                    <td>
                      <code>{group.name}</code>
                    </td>
                    <td>
                      {group.allowedModels.length
                        ? group.allowedModels.join(', ')
                        : t('access_groups.all_models')}
                    </td>
                    <td>
                      {group.allowedAuths.length
                        ? t('access_groups.pool_count', { count: group.allowedAuths.length })
                        : t('access_groups.all_auths')}
                    </td>
                    <td>{summarizeLimits(group)}</td>
                    <td className={styles.totalCell}>
                      {formatTokens(group.usage.tokens)}
                      <small className={styles.usageSub}>
                        {t('access_groups.usage_sub', {
                          requests: group.usage.requests,
                          failed: group.usage.failed,
                        })}
                      </small>
                    </td>
                    <td>{formatDateTimeValue(group.updatedAt) || DASH}</td>
                    <td>
                      <div className={styles.rowActions}>
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => setSheet({ open: true, group })}
                        >
                          {t('common.edit')}
                        </Button>
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => setDeleting(group)}
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

      <GroupEditSheet
        open={sheet.open}
        group={sheet.group}
        auths={auths}
        models={models.map((model) => model.name)}
        modelsLoading={modelsLoading}
        modelsError={modelsError}
        mutating={mutating}
        onClose={() => setSheet({ open: false, group: null })}
        onSubmit={async (payload) => {
          setMutating(true);
          try {
            await accessControlApi.putGroup(payload);
            setSheet({ open: false, group: null });
            await load();
          } catch (saveError: unknown) {
            setError(saveError instanceof Error ? saveError.message : t('common.unknown_error'));
          } finally {
            setMutating(false);
          }
        }}
      />

      <Modal
        open={deleting !== null}
        title={t('access_groups.delete_title', { name: deleting?.name ?? '' })}
        onClose={() => setDeleting(null)}
        closeDisabled={mutating}
        footer={
          <>
            <Button variant="secondary" onClick={() => setDeleting(null)} disabled={mutating}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => void handleDelete()} disabled={mutating}>
              {mutating ? t('common.loading') : t('common.delete')}
            </Button>
          </>
        }
      >
        <p className={styles.deleteHint}>{t('access_groups.delete_hint')}</p>
      </Modal>
    </div>
  );
}
