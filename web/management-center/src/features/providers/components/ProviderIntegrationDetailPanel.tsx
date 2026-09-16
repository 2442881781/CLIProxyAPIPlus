import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import commandCodeLogo from '@/assets/icons/commandcode.png';
import devinLogo from '@/assets/icons/devin.svg';
import openCodeLogo from '@/assets/icons/opencode.png';
import zhipuLogo from '@/assets/icons/zhipu.png';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Sheet } from '@/components/ui/Sheet';
import { authFilesApi, modelsApi, pluginsApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import type { AuthFileItem, OAuthModelAliasEntry, PluginListEntry } from '@/types';
import type { ProviderIntegrationID } from './ProviderCategoryList';
import styles from './ProviderIntegrationDetailPanel.module.scss';
import pluginStyles from './ProviderPluginCredentialsPanel.module.scss';
import { ModelPolicyEditor } from './ModelPolicyEditor';
import { modelPolicySignature, normalizeModelPolicy, type ModelPolicy } from '../modelPolicy';

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
  const { showNotification } = useNotificationStore();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const config = useConfigStore((state) => state.config);
  const [plugins, setPlugins] = useState<PluginListEntry[]>([]);
  const [authFiles, setAuthFiles] = useState<AuthFileItem[]>([]);
  const [checking, setChecking] = useState(true);
  const [policy, setPolicy] = useState<ModelPolicy>(normalizeModelPolicy());
  const [policyDraft, setPolicyDraft] = useState<ModelPolicy>(normalizeModelPolicy());
  const [policyOpen, setPolicyOpen] = useState(false);
  const [policySaving, setPolicySaving] = useState(false);
  const [policyCatalog, setPolicyCatalog] = useState<string[]>([]);
  const [policyCatalogLoading, setPolicyCatalogLoading] = useState(false);
  const [policyCatalogError, setPolicyCatalogError] = useState('');

  useEffect(() => {
    let active = true;
    if (!connected) {
      setChecking(false);
      return () => {
        active = false;
      };
    }

    setChecking(true);
    void Promise.allSettled([
      pluginsApi.list(),
      authFilesApi.list(),
      id === 'devin'
        ? authFilesApi.getOauthAllowedModels()
        : Promise.resolve<Record<string, string[]>>({}),
      id === 'devin'
        ? authFilesApi.getOauthModelAlias()
        : Promise.resolve<Record<string, OAuthModelAliasEntry[]>>({}),
    ]).then(
      ([pluginResult, authResult, allowedResult, aliasResult]) => {
        if (!active) return;
        if (pluginResult.status === 'fulfilled') setPlugins(pluginResult.value.plugins);
        if (authResult.status === 'fulfilled') setAuthFiles(authResult.value.files ?? []);
        if (
          id === 'devin' &&
          allowedResult.status === 'fulfilled' &&
          aliasResult.status === 'fulfilled'
        ) {
          setPolicy(
            normalizeModelPolicy({
              allowedModels: allowedResult.value.devin ?? [],
              aliases: aliasResult.value.devin ?? [],
            })
          );
        }
        setChecking(false);
      }
    );

    return () => {
      active = false;
    };
  }, [connected, id, refreshKey]);

  const openPolicy = () => {
    setPolicyDraft(normalizeModelPolicy(policy));
    setPolicyOpen(true);
    setPolicyCatalogLoading(true);
    setPolicyCatalogError('');
    void modelsApi
      .fetchProviderModels('devin')
      .then((models) => setPolicyCatalog(models.map((model) => model.name)))
      .catch((error) =>
        setPolicyCatalogError(error instanceof Error ? error.message : String(error))
      )
      .finally(() => setPolicyCatalogLoading(false));
  };

  const savePolicy = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const normalized = normalizeModelPolicy(policyDraft);
    if (normalized.aliases.some((entry) => !entry.name.trim() || !entry.alias.trim())) {
      showNotification(t('providersPage.integrations.credentials.modelFieldsRequired'), 'error');
      return;
    }
    const aliases = normalized.aliases.map((entry) => entry.alias.toLowerCase());
    if (new Set(aliases).size !== aliases.length) {
      showNotification(t('providersPage.integrations.credentials.duplicateModelAlias'), 'error');
      return;
    }
    setPolicySaving(true);
    try {
      await Promise.all([
        normalized.allowedModels.length
          ? authFilesApi.saveOauthAllowedModels('devin', normalized.allowedModels)
          : policy.allowedModels.length
            ? authFilesApi.deleteOauthAllowedEntry('devin')
            : Promise.resolve(),
        normalized.aliases.length
          ? authFilesApi.saveOauthModelAlias(
              'devin',
              normalized.aliases.map((entry) => ({
                name: entry.name.trim(),
                alias: entry.alias.trim(),
                ...(entry.fork ? { fork: true } : {}),
                ...(entry.forceMapping ? { 'force-mapping': true } : {}),
              }))
            )
          : policy.aliases.length
            ? authFilesApi.deleteOauthModelAlias('devin')
            : Promise.resolve(),
      ]);
      setPolicy(normalized);
      setPolicyOpen(false);
      showNotification(t('providersPage.toast.updated'), 'success');
    } catch (error) {
      showNotification(error instanceof Error ? error.message : String(error), 'error');
    } finally {
      setPolicySaving(false);
    }
  };

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
        <div className={styles.actions}>
          {id === 'devin' ? (
            <Button variant="secondary" onClick={openPolicy}>
              {t('providersPage.integrations.credentials.editModels')}
            </Button>
          ) : null}
          <Button variant="secondary" onClick={detail.action}>
            {detail.actionLabel}
          </Button>
        </div>
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
      <Sheet
        open={policyOpen}
        onClose={() => !policySaving && setPolicyOpen(false)}
        closeDisabled={policySaving}
        eyebrow={t('providersPage.integrations.modelPolicy.eyebrow')}
        title={`${t('providersPage.integrations.credentials.modelsTitle')} · ${t('providersPage.providerNames.devin')}`}
        description={t('providersPage.integrations.modelPolicy.description')}
        footer={
          <>
            <button
              type="button"
              className={pluginStyles.footerButton}
              onClick={() => setPolicyOpen(false)}
              disabled={policySaving}
            >
              {t('providersPage.actions.cancel')}
            </button>
            <button
              type="submit"
              form="devin-model-policy-form"
              className={`${pluginStyles.footerButton} ${pluginStyles.footerPrimary}`}
              disabled={
                policySaving ||
                modelPolicySignature(policyDraft) === modelPolicySignature(policy)
              }
            >
              {t('providersPage.actions.save')}
            </button>
          </>
        }
      >
        <form id="devin-model-policy-form" onSubmit={savePolicy}>
          <ModelPolicyEditor
            value={policyDraft}
            catalog={policyCatalog}
            catalogLoading={policyCatalogLoading}
            catalogError={policyCatalogError}
            disabled={policySaving}
            onChange={setPolicyDraft}
          />
        </form>
      </Sheet>
    </Card>
  );
}
