import { useCallback, useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconPlus, IconRefreshCw } from '@/components/ui/icons';
import { OAuthModelAliasCard } from '@/features/authFiles/components/OAuthModelAliasCard';
import { useAuthFilesOauth } from '@/features/authFiles/hooks/useAuthFilesOauth';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { authFilesApi } from '@/services/api';
import { useAuthStore } from '@/stores';
import type { AuthFileItem } from '@/types';
import styles from './ModelRoutesPage.module.scss';

export function ModelRoutesPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [viewMode, setViewMode] = useState<'diagram' | 'list'>('diagram');

  const {
    modelAlias,
    modelAliasError,
    allProviderModels,
    loadModelAlias,
    deleteModelAlias,
    handleMappingUpdate,
    handleDeleteLink,
    handleToggleFork,
    handleRenameAlias,
    handleDeleteAlias,
  } = useAuthFilesOauth({ viewMode, files });

  const load = useCallback(async () => {
    if (!connected) {
      setLoading(false);
      return;
    }

    setLoading(true);
    setError('');
    const [filesResult] = await Promise.allSettled([authFilesApi.list(), loadModelAlias()]);
    if (filesResult.status === 'fulfilled') {
      setFiles(filesResult.value?.files ?? []);
    } else {
      const message =
        filesResult.reason instanceof Error
          ? filesResult.reason.message
          : t('model_routes.load_failed');
      setError(message);
    }
    setLoading(false);
  }, [connected, loadModelAlias, t]);

  useHeaderRefresh(load, connected);

  useEffect(() => {
    void load();
  }, [load]);

  const openEditor = useCallback(
    (provider?: string) => {
      const params = new URLSearchParams();
      const normalizedProvider = String(provider ?? '').trim();
      if (normalizedProvider) params.set('provider', normalizedProvider);
      const search = params.toString();
      navigate(`/model-routes/edit${search ? `?${search}` : ''}`, {
        state: { fromModelRoutes: true },
      });
    },
    [navigate]
  );

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <div className={styles.eyebrow}>{t('model_routes.eyebrow')}</div>
          <h1>{t('model_routes.title')}</h1>
          <p>{t('model_routes.description')}</p>
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
          <Button size="sm" onClick={() => openEditor()} disabled={!connected}>
            <span className={styles.buttonContent}>
              <IconPlus size={15} />
              {t('model_routes.create')}
            </span>
          </Button>
        </div>
      </header>

      <div className={styles.info}>{t('model_routes.direct_route_hint')}</div>

      {error ? (
        <div className={styles.error} role="alert">
          {error}
        </div>
      ) : null}

      <OAuthModelAliasCard
        disableControls={!connected}
        viewMode={viewMode}
        onViewModeChange={setViewMode}
        onRetry={loadModelAlias}
        onAdd={() => openEditor()}
        onEditProvider={openEditor}
        onDeleteProvider={deleteModelAlias}
        modelAliasError={modelAliasError}
        modelAlias={modelAlias}
        allProviderModels={allProviderModels}
        onUpdate={handleMappingUpdate}
        onDeleteLink={handleDeleteLink}
        onToggleFork={handleToggleFork}
        onRenameAlias={handleRenameAlias}
        onDeleteAlias={handleDeleteAlias}
      />
    </div>
  );
}
