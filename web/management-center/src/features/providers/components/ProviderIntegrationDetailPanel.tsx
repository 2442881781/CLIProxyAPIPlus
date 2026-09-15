import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import commandCodeLogo from '@/assets/icons/commandcode.png';
import devinLogo from '@/assets/icons/devin.svg';
import openCodeLogo from '@/assets/icons/opencode.png';
import zhipuLogo from '@/assets/icons/zhipu.png';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { authFilesApi, pluginsApi } from '@/services/api';
import { useAuthStore, useConfigStore } from '@/stores';
import type { AuthFileItem, PluginListEntry } from '@/types';
import type { ProviderIntegrationID } from './ProviderCategoryList';
import styles from './ProviderIntegrationDetailPanel.module.scss';

interface ProviderIntegrationDetailPanelProps {
  id: ProviderIntegrationID;
  refreshKey?: string;
  onEditZhipu: () => void;
  onManagePlugins: () => void;
  onManageAuthFiles: () => void;
  onStatsChange?: (total: number, active: number) => void;
}

type IntegrationState = 'ready' | 'attention' | 'available' | 'checking';

const logos: Record<ProviderIntegrationID, string> = {
  commandcode: commandCodeLogo,
  opencode: openCodeLogo,
  zhipu: zhipuLogo,
  devin: devinLogo,
};

const isDevinCredential = (file: AuthFileItem): boolean =>
  [file.type, file.provider].some(
    (value) =>
      String(value ?? '')
        .trim()
        .toLowerCase() === 'devin'
  );

const isActiveAuthFile = (file: AuthFileItem): boolean =>
  file.disabled !== true && file.unavailable !== true && file.status !== 'disabled';

export function ProviderIntegrationDetailPanel({
  id,
  refreshKey,
  onEditZhipu,
  onManagePlugins,
  onManageAuthFiles,
  onStatsChange,
}: ProviderIntegrationDetailPanelProps) {
  const { t } = useTranslation();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const config = useConfigStore((state) => state.config);
  const [plugins, setPlugins] = useState<PluginListEntry[]>([]);
  const [authFiles, setAuthFiles] = useState<AuthFileItem[]>([]);
  const [checking, setChecking] = useState(true);

  useEffect(() => {
    let active = true;
    if (!connected) {
      setChecking(false);
      return () => {
        active = false;
      };
    }

    setChecking(true);
    void Promise.allSettled([pluginsApi.list(), authFilesApi.list()]).then(
      ([pluginResult, authResult]) => {
        if (!active) return;
        if (pluginResult.status === 'fulfilled') setPlugins(pluginResult.value.plugins);
        if (authResult.status === 'fulfilled') setAuthFiles(authResult.value.files ?? []);
        setChecking(false);
      }
    );

    return () => {
      active = false;
    };
  }, [connected, refreshKey]);

  const detail = useMemo(() => {
    if (id === 'commandcode' || id === 'opencode') {
      const pluginID = id === 'commandcode' ? 'commandcode' : 'opencode-go-cliproxyapi';
      const plugin = plugins.find((item) => item.id === pluginID);
      const state: IntegrationState = checking
        ? 'checking'
        : plugin?.registered && plugin.effectiveEnabled
          ? 'ready'
          : plugin
            ? 'attention'
            : 'available';
      return {
        state,
        total: plugin ? 1 : 0,
        active: state === 'ready' ? 1 : 0,
        rows: [
          [t('providersPage.integrations.details.providerId'), pluginID],
          [t('providersPage.integrations.details.version'), plugin?.metadata?.version || '—'],
          [t('providersPage.integrations.details.path'), plugin?.path || '—'],
        ],
        action: onManagePlugins,
        actionLabel: t('providersPage.integrations.actions.plugins'),
      };
    }

    if (id === 'zhipu') {
      const provider = config?.openaiCompatibility?.find((item) =>
        item.name.toLowerCase().includes('zhipu glm')
      );
      const keyCount = provider?.apiKeyEntries?.length ?? 0;
      const state: IntegrationState = checking
        ? 'checking'
        : provider && !provider.disabled && keyCount > 0
          ? 'ready'
          : provider
            ? 'attention'
            : 'available';
      return {
        state,
        total: keyCount,
        active: state === 'ready' ? keyCount : 0,
        rows: [
          [t('providersPage.integrations.details.baseUrl'), provider?.baseUrl || '—'],
          [t('providersPage.integrations.details.prefix'), provider?.prefix || '—'],
          [t('providersPage.integrations.details.models'), String(provider?.models?.length ?? 0)],
          [t('providersPage.integrations.details.keys'), String(keyCount)],
        ],
        action: onEditZhipu,
        actionLabel: t('providersPage.integrations.actions.zhipu'),
      };
    }

    const files = authFiles.filter(isDevinCredential);
    const activeCount = files.filter(isActiveAuthFile).length;
    return {
      state: checking
        ? ('checking' as const)
        : activeCount > 0
          ? ('ready' as const)
          : ('available' as const),
      total: files.length,
      active: activeCount,
      rows: [
        [t('providersPage.integrations.details.providerId'), 'devin'],
        [t('providersPage.integrations.details.credentials'), `${activeCount}/${files.length}`],
      ],
      action: onManageAuthFiles,
      actionLabel: t('providersPage.integrations.actions.authFiles'),
    };
  }, [
    authFiles,
    checking,
    config?.openaiCompatibility,
    id,
    onEditZhipu,
    onManageAuthFiles,
    onManagePlugins,
    plugins,
    t,
  ]);

  useEffect(() => {
    onStatsChange?.(detail.total, detail.active);
  }, [detail.active, detail.total, onStatsChange]);

  return (
    <Card className={styles.panel}>
      <div className={styles.header}>
        <img src={logos[id]} alt="" aria-hidden="true" className={styles.logo} />
        <div className={styles.heading}>
          <div className={styles.titleRow}>
            <h2>{t(`providersPage.providerNames.${id}`)}</h2>
            <span className={`${styles.badge} ${styles[detail.state]}`}>
              {t(`providersPage.integrations.states.${detail.state}`)}
            </span>
          </div>
          <p>{t(`providersPage.integrations.items.${id}.description`)}</p>
        </div>
        <Button variant="secondary" onClick={detail.action}>
          {detail.actionLabel}
        </Button>
      </div>

      <dl className={styles.details}>
        {detail.rows.map(([label, value]) => (
          <div className={styles.detailRow} key={label}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>

      {id === 'devin' && detail.state !== 'ready' ? (
        <div className={styles.command}>
          <span>{t('providersPage.integrations.details.oauthCli')}</span>
          <code>go run ./cmd/server --config config.yaml --devin-login</code>
        </div>
      ) : null}
    </Card>
  );
}
