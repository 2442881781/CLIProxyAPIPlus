import {
  useCallback,
  useEffect,
  useImperativeHandle,
  useMemo,
  useState,
  type FormEvent,
  type Ref,
} from 'react';
import { useTranslation } from 'react-i18next';
import commandCodeLogo from '@/assets/icons/commandcode.png';
import openCodeLogo from '@/assets/icons/opencode.png';
import { Sheet } from '@/components/ui/Sheet';
import {
  IconAlertTriangle,
  IconCheckCircle2,
  IconLoader2,
  IconPencil,
  IconPlus,
  IconSearch,
  IconTrash2,
} from '@/components/ui/icons';
import { authFilesApi, modelsApi, pluginsApi } from '@/services/api';
import { useNotificationStore } from '@/stores';
import type { OAuthModelAliasEntry, PluginConfigObject, PluginListEntry } from '@/types';
import {
  buildCommandCodeModelsPatch,
  COMMANDCODE_DEFAULT_MODELS,
  readCommandCodeModels,
  type CommandCodeModelEntry,
} from '../commandCodeModels';
import { modelPolicySignature, normalizeModelPolicy, type ModelPolicy } from '../modelPolicy';
import { ModelPolicyEditor } from './ModelPolicyEditor';
import styles from './ProviderPluginCredentialsPanel.module.scss';

export type PluginCredentialProvider = 'commandcode' | 'opencode';

export interface ProviderPluginCredentialsPanelHandle {
  openCreate: () => void;
}

interface ProviderPluginCredentialsPanelProps {
  provider: PluginCredentialProvider;
  refreshKey?: string;
  disabled?: boolean;
  onChanged: () => void | Promise<void>;
  onStatsChange?: (total: number, active: number) => void;
  ref?: Ref<ProviderPluginCredentialsPanelHandle>;
}

interface CredentialEntry {
  key: string;
  weight: number;
  proxyUrl: string;
}

interface EditorState {
  open: boolean;
  index: number | null;
  key: string;
  weight: string;
  proxyUrl: string;
}

interface ModelEditorState {
  open: boolean;
  models: CommandCodeModelEntry[];
  usesDefaults: boolean;
}

interface PolicyEditorState {
  open: boolean;
  policy: ModelPolicy;
}

const PLUGIN_IDS: Record<PluginCredentialProvider, string> = {
  commandcode: 'commandcode',
  opencode: 'opencode-go-cliproxyapi',
};

const LOGOS: Record<PluginCredentialProvider, string> = {
  commandcode: commandCodeLogo,
  opencode: openCodeLogo,
};

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const stringValue = (value: unknown): string => (typeof value === 'string' ? value : '');

const numberValue = (value: unknown, fallback = 1): number =>
  typeof value === 'number' && Number.isFinite(value) ? value : fallback;

const readEntries = (
  provider: PluginCredentialProvider,
  config: PluginConfigObject
): CredentialEntry[] => {
  if (provider === 'commandcode') {
    const pooled = Array.isArray(config.api_keys)
      ? config.api_keys.flatMap((item) => {
          if (!isRecord(item)) return [];
          const key = stringValue(item.key).trim();
          if (!key) return [];
          return [
            {
              key,
              weight: numberValue(item.weight),
              proxyUrl: stringValue(item.proxy_url),
            },
          ];
        })
      : [];
    if (pooled.length > 0) return pooled;
    const legacyKey = stringValue(config.api_key).trim();
    return legacyKey ? [{ key: legacyKey, weight: 1, proxyUrl: '' }] : [];
  }

  return Array.isArray(config['api-keys'])
    ? config['api-keys'].flatMap((item) => {
        if (!isRecord(item)) return [];
        const key = stringValue(item.value).trim();
        return key ? [{ key, weight: 1, proxyUrl: '' }] : [];
      })
    : [];
};

const maskKey = (key: string): string => {
  if (key.startsWith('${') && key.endsWith('}')) return key;
  if (key.length <= 8) return '••••••••';
  return `${key.slice(0, 4)}••••${key.slice(-4)}`;
};

const emptyEditor = (): EditorState => ({
  open: false,
  index: null,
  key: '',
  weight: '1',
  proxyUrl: '',
});

const emptyModelEditor = (): ModelEditorState => ({
  open: false,
  models: [],
  usesDefaults: true,
});

const emptyPolicyEditor = (): PolicyEditorState => ({
  open: false,
  policy: normalizeModelPolicy(),
});

const POLICY_PROVIDER_KEYS: Record<Exclude<PluginCredentialProvider, 'commandcode'>, string> = {
  opencode: 'opencode-go',
};

const toApiAliases = (aliases: OAuthModelAliasEntry[]) =>
  aliases.map((entry) => ({
    name: entry.name.trim(),
    alias: entry.alias.trim(),
    ...(entry.fork ? { fork: true } : {}),
    ...(entry.forceMapping ? { 'force-mapping': true } : {}),
  }));

export function ProviderPluginCredentialsPanel({
  provider,
  refreshKey,
  disabled = false,
  onChanged,
  onStatsChange,
  ref,
}: ProviderPluginCredentialsPanelProps) {
  const { t } = useTranslation();
  const { showConfirmation, showNotification } = useNotificationStore();
  const [config, setConfig] = useState<PluginConfigObject>({});
  const [plugin, setPlugin] = useState<PluginListEntry | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [filter, setFilter] = useState('');
  const [editor, setEditor] = useState<EditorState>(emptyEditor);
  const [modelEditor, setModelEditor] = useState<ModelEditorState>(emptyModelEditor);
  const [policy, setPolicy] = useState<ModelPolicy>(normalizeModelPolicy());
  const [policyEditor, setPolicyEditor] = useState<PolicyEditorState>(emptyPolicyEditor);
  const [policyCatalog, setPolicyCatalog] = useState<string[]>([]);
  const [policyCatalogLoading, setPolicyCatalogLoading] = useState(false);
  const [policyCatalogError, setPolicyCatalogError] = useState('');

  const pluginID = PLUGIN_IDS[provider];
  const entries = useMemo(() => readEntries(provider, config), [config, provider]);
  const commandCodeModels = useMemo(() => readCommandCodeModels(config), [config]);
  const visibleEntries = useMemo(() => {
    const query = filter.trim().toLowerCase();
    if (!query) return entries;
    return entries.filter(
      (entry) =>
        maskKey(entry.key).toLowerCase().includes(query) ||
        entry.proxyUrl.toLowerCase().includes(query)
    );
  }, [entries, filter]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const policyProvider = provider === 'opencode' ? POLICY_PROVIDER_KEYS.opencode : '';
      const [nextConfig, pluginList, allowedMap, aliasMap] = await Promise.all([
        pluginsApi.getConfig(pluginID),
        pluginsApi.list(),
        policyProvider
          ? authFilesApi.getOauthAllowedModels()
          : Promise.resolve<Record<string, string[]>>({}),
        policyProvider
          ? authFilesApi.getOauthModelAlias()
          : Promise.resolve<Record<string, OAuthModelAliasEntry[]>>({}),
      ]);
      const nextPlugin = pluginList.plugins.find((item) => item.id === pluginID) ?? null;
      const nextEntries = readEntries(provider, nextConfig);
      setConfig(nextConfig);
      setPlugin(nextPlugin);
      if (policyProvider) {
        setPolicy(
          normalizeModelPolicy({
            allowedModels: allowedMap[policyProvider] ?? [],
            aliases: aliasMap[policyProvider] ?? [],
          })
        );
      }
      onStatsChange?.(
        nextEntries.length,
        nextPlugin?.registered && nextPlugin.effectiveEnabled ? nextEntries.length : 0
      );
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      showNotification(message, 'error');
    } finally {
      setLoading(false);
    }
  }, [onStatsChange, pluginID, provider, showNotification]);

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  const openCreate = useCallback(() => {
    setEditor({ ...emptyEditor(), open: true });
  }, []);

  useImperativeHandle(ref, () => ({ openCreate }), [openCreate]);

  const openEdit = (index: number) => {
    const entry = entries[index];
    if (!entry) return;
    setEditor({
      open: true,
      index,
      key: entry.key,
      weight: String(entry.weight),
      proxyUrl: entry.proxyUrl,
    });
  };

  const closeEditor = () => {
    if (saving) return;
    setEditor(emptyEditor());
  };

  const patchEntries = async (nextEntries: CredentialEntry[]) => {
    const patch: PluginConfigObject =
      provider === 'commandcode'
        ? {
            api_key: null,
            api_keys: nextEntries.map((entry) => ({
              key: entry.key,
              weight: entry.weight,
              ...(entry.proxyUrl ? { proxy_url: entry.proxyUrl } : {}),
            })),
          }
        : {
            'api-keys': nextEntries.map((entry) => ({ value: entry.key })),
          };
    await pluginsApi.patchConfig(pluginID, patch);
    await load();
    await onChanged();
  };

  const saveEditor = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (saving || disabled) return;
    const key = editor.key.trim();
    if (!key) {
      showNotification(t('providersPage.form.validation.apiKeyRequired'), 'error');
      return;
    }
    if (entries.some((entry, index) => entry.key === key && index !== editor.index)) {
      showNotification(t('providersPage.integrations.credentials.duplicateKey'), 'error');
      return;
    }

    const parsedWeight = Number(editor.weight);
    if (provider === 'commandcode' && !Number.isSafeInteger(parsedWeight)) {
      showNotification(t('providersPage.form.validation.weightInteger'), 'error');
      return;
    }

    const nextEntry: CredentialEntry = {
      key,
      weight: provider === 'commandcode' ? parsedWeight : 1,
      proxyUrl: provider === 'commandcode' ? editor.proxyUrl.trim() : '',
    };
    const nextEntries =
      editor.index === null
        ? [...entries, nextEntry]
        : entries.map((entry, index) => (index === editor.index ? nextEntry : entry));

    setSaving(true);
    try {
      await patchEntries(nextEntries);
      showNotification(
        t(editor.index === null ? 'providersPage.toast.created' : 'providersPage.toast.updated'),
        'success'
      );
      setEditor(emptyEditor());
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      showNotification(message, 'error');
    } finally {
      setSaving(false);
    }
  };

  const deleteEntry = (index: number) => {
    const entry = entries[index];
    if (!entry) return;
    showConfirmation({
      title: t('providersPage.delete.title'),
      message: t('providersPage.delete.confirm', { name: maskKey(entry.key) }),
      variant: 'danger',
      confirmText: t('providersPage.actions.delete'),
      onConfirm: async () => {
        setSaving(true);
        try {
          await patchEntries(entries.filter((_, entryIndex) => entryIndex !== index));
          showNotification(t('providersPage.toast.deleted'), 'success');
        } catch (error) {
          const message = error instanceof Error ? error.message : String(error);
          showNotification(message, 'error');
        } finally {
          setSaving(false);
        }
      },
    });
  };

  const openModelEditor = () => {
    setModelEditor({
      open: true,
      models: commandCodeModels.models.map((model) => ({ ...model })),
      usesDefaults: commandCodeModels.usesDefaults,
    });
  };

  const closeModelEditor = () => {
    if (saving) return;
    setModelEditor(emptyModelEditor());
  };

  const updateModel = (index: number, patch: Partial<CommandCodeModelEntry>) => {
    setModelEditor((state) => ({
      ...state,
      usesDefaults: false,
      models: state.models.map((model, modelIndex) =>
        modelIndex === index ? { ...model, ...patch } : model
      ),
    }));
  };

  const addModel = () => {
    setModelEditor((state) => ({
      ...state,
      usesDefaults: false,
      models: [...state.models, { alias: '', name: '', displayName: '' }],
    }));
  };

  const removeModel = (index: number) => {
    setModelEditor((state) => ({
      ...state,
      usesDefaults: false,
      models: state.models.filter((_, modelIndex) => modelIndex !== index),
    }));
  };

  const restoreDefaultModels = () => {
    setModelEditor((state) => ({
      ...state,
      usesDefaults: true,
      models: COMMANDCODE_DEFAULT_MODELS.map((model) => ({ ...model })),
    }));
  };

  const saveModels = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (saving || disabled) return;

    if (!modelEditor.usesDefaults && modelEditor.models.length === 0) {
      showNotification(t('providersPage.integrations.credentials.modelRequired'), 'error');
      return;
    }
    if (
      !modelEditor.usesDefaults &&
      modelEditor.models.some((model) => !model.alias.trim() || !model.name.trim())
    ) {
      showNotification(t('providersPage.integrations.credentials.modelFieldsRequired'), 'error');
      return;
    }
    const aliases = modelEditor.models.map((model) => model.alias.trim().toLowerCase());
    if (!modelEditor.usesDefaults && new Set(aliases).size !== aliases.length) {
      showNotification(t('providersPage.integrations.credentials.duplicateModelAlias'), 'error');
      return;
    }

    setSaving(true);
    try {
      await pluginsApi.patchConfig(
        pluginID,
        buildCommandCodeModelsPatch(modelEditor.models, modelEditor.usesDefaults)
      );
      await load();
      await onChanged();
      showNotification(t('providersPage.toast.updated'), 'success');
      setModelEditor(emptyModelEditor());
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      showNotification(message, 'error');
    } finally {
      setSaving(false);
    }
  };

  const openPolicyEditor = () => {
    setPolicyEditor({ open: true, policy: normalizeModelPolicy(policy) });
    setPolicyCatalogLoading(true);
    setPolicyCatalogError('');
    void modelsApi
      .fetchProviderModels(POLICY_PROVIDER_KEYS.opencode)
      .then((models) => setPolicyCatalog(models.map((model) => model.name)))
      .catch((error) => {
        setPolicyCatalogError(error instanceof Error ? error.message : String(error));
      })
      .finally(() => setPolicyCatalogLoading(false));
  };

  const closePolicyEditor = () => {
    if (saving) return;
    setPolicyEditor(emptyPolicyEditor());
  };

  const savePolicy = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (saving || disabled || provider !== 'opencode') return;
    const normalized = normalizeModelPolicy(policyEditor.policy);
    if (normalized.aliases.some((entry) => !entry.name.trim() || !entry.alias.trim())) {
      showNotification(t('providersPage.integrations.credentials.modelFieldsRequired'), 'error');
      return;
    }
    const aliases = normalized.aliases.map((entry) => entry.alias.toLowerCase());
    if (new Set(aliases).size !== aliases.length) {
      showNotification(t('providersPage.integrations.credentials.duplicateModelAlias'), 'error');
      return;
    }

    const policyProvider = POLICY_PROVIDER_KEYS.opencode;
    setSaving(true);
    try {
      await Promise.all([
        normalized.allowedModels.length
          ? authFilesApi.saveOauthAllowedModels(policyProvider, normalized.allowedModels)
          : policy.allowedModels.length
            ? authFilesApi.deleteOauthAllowedEntry(policyProvider)
            : Promise.resolve(),
        normalized.aliases.length
          ? authFilesApi.saveOauthModelAlias(policyProvider, toApiAliases(normalized.aliases))
          : policy.aliases.length
            ? authFilesApi.deleteOauthModelAlias(policyProvider)
            : Promise.resolve(),
      ]);
      setPolicy(normalized);
      setPolicyEditor(emptyPolicyEditor());
      await load();
      await onChanged();
      showNotification(t('providersPage.toast.updated'), 'success');
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      showNotification(message, 'error');
    } finally {
      setSaving(false);
    }
  };

  const ready = plugin?.registered === true && plugin.effectiveEnabled;
  const providerName = t(`providersPage.providerNames.${provider}`);

  return (
    <>
      <section className={styles.panel}>
        <div className={styles.header}>
          <div className={styles.titleRow}>
            <img src={LOGOS[provider]} alt="" aria-hidden="true" className={styles.logo} />
            <div>
              <h2 className={styles.title}>{providerName}</h2>
              <p className={styles.description}>
                {t(`providersPage.integrations.items.${provider}.description`)}
              </p>
            </div>
          </div>
          <div className={styles.searchWrap}>
            <IconSearch size={16} className={styles.searchIcon} />
            <input
              type="search"
              className={styles.searchInput}
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              placeholder={t('providersPage.table.filterPlaceholder')}
            />
          </div>
        </div>

        {loading ? (
          <div className={styles.empty}>{t('providersPage.integrations.states.checking')}</div>
        ) : visibleEntries.length === 0 ? (
          <div className={styles.empty}>
            <div>{t('providersPage.table.empty')}</div>
            <button
              type="button"
              className={styles.newButton}
              onClick={openCreate}
              disabled={disabled || saving}
            >
              <IconPlus size={16} />
              {t('providersPage.actions.new')}
            </button>
          </div>
        ) : (
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('providersPage.table.key')}</th>
                  {provider === 'commandcode' ? (
                    <>
                      <th>{t('providersPage.form.weight')}</th>
                      <th>{t('providersPage.form.proxyUrl')}</th>
                    </>
                  ) : null}
                  <th>{t('providersPage.table.status')}</th>
                  <th className={styles.actionsHead}>{t('providersPage.table.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {visibleEntries.map((entry) => {
                  const sourceIndex = entries.indexOf(entry);
                  return (
                    <tr key={`${entry.key}:${sourceIndex}`}>
                      <td className={styles.key}>{maskKey(entry.key)}</td>
                      {provider === 'commandcode' ? (
                        <>
                          <td>{entry.weight}</td>
                          <td className={styles.proxy}>{entry.proxyUrl || '—'}</td>
                        </>
                      ) : null}
                      <td>
                        <span className={ready ? styles.statusReady : styles.statusAttention}>
                          {ready ? <IconCheckCircle2 size={14} /> : <IconAlertTriangle size={14} />}
                          {t(
                            ready
                              ? 'providersPage.integrations.states.ready'
                              : 'providersPage.integrations.states.attention'
                          )}
                        </span>
                      </td>
                      <td>
                        <div className={styles.actions}>
                          <button
                            type="button"
                            className={styles.iconButton}
                            onClick={() => openEdit(sourceIndex)}
                            disabled={disabled || saving}
                            aria-label={t('providersPage.actions.edit')}
                            title={t('providersPage.actions.edit')}
                          >
                            <IconPencil size={16} />
                          </button>
                          <button
                            type="button"
                            className={`${styles.iconButton} ${styles.deleteButton}`}
                            onClick={() => deleteEntry(sourceIndex)}
                            disabled={disabled || saving}
                            aria-label={t('providersPage.actions.delete')}
                            title={t('providersPage.actions.delete')}
                          >
                            <IconTrash2 size={16} />
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        {provider === 'commandcode' && !loading ? (
          <div className={styles.modelSection}>
            <div className={styles.sectionHead}>
              <div>
                <h3>{t('providersPage.integrations.credentials.modelsTitle')}</h3>
                <p>{t('providersPage.integrations.credentials.modelsHint')}</p>
              </div>
              <button
                type="button"
                className={styles.newButton}
                onClick={openModelEditor}
                disabled={disabled || saving}
              >
                <IconPencil size={15} />
                {t('providersPage.integrations.credentials.editModels')}
              </button>
            </div>
            <div className={styles.modelList}>
              {commandCodeModels.models.map((model) => (
                <div className={styles.modelItem} key={`${model.alias}:${model.name}`}>
                  <span className={styles.modelAlias}>{model.alias}</span>
                  <span className={styles.modelArrow}>→</span>
                  <span className={styles.modelUpstream}>{model.name || model.alias}</span>
                </div>
              ))}
            </div>
            <div className={styles.modelSource}>
              {t(
                commandCodeModels.usesDefaults
                  ? 'providersPage.integrations.credentials.modelsDefault'
                  : 'providersPage.integrations.credentials.modelsRestricted'
              )}
            </div>
          </div>
        ) : provider === 'opencode' && !loading ? (
          <div className={styles.modelSection}>
            <div className={styles.sectionHead}>
              <div>
                <h3>{t('providersPage.integrations.credentials.modelsTitle')}</h3>
                <p>{t('providersPage.integrations.modelPolicy.summaryHint')}</p>
              </div>
              <button
                type="button"
                className={styles.newButton}
                onClick={openPolicyEditor}
                disabled={disabled || saving}
              >
                <IconPencil size={15} />
                {t('providersPage.integrations.credentials.editModels')}
              </button>
            </div>
            <div className={styles.modelList}>
              {policy.allowedModels.map((model) => (
                <div className={styles.modelItem} key={model}>
                  <span className={styles.modelAlias}>{model}</span>
                </div>
              ))}
              {policy.aliases.map((entry) => (
                <div className={styles.modelItem} key={`${entry.alias}:${entry.name}`}>
                  <span className={styles.modelAlias}>{entry.alias}</span>
                  <span className={styles.modelArrow}>→</span>
                  <span className={styles.modelUpstream}>{entry.name}</span>
                </div>
              ))}
            </div>
            <div className={styles.modelSource}>
              {policy.allowedModels.length
                ? t('providersPage.integrations.modelPolicy.restrictedCount', {
                    count: policy.allowedModels.length,
                  })
                : t('providersPage.integrations.modelPolicy.unrestrictedHint')}
            </div>
          </div>
        ) : null}
      </section>

      <Sheet
        open={editor.open}
        onClose={closeEditor}
        closeDisabled={saving}
        eyebrow={
          editor.index === null
            ? t('providersPage.form.createEyebrow')
            : t('providersPage.form.editEyebrow')
        }
        title={`${
          editor.index === null ? t('providersPage.actions.new') : t('providersPage.actions.edit')
        } · ${providerName}`}
        description={t(`providersPage.integrations.items.${provider}.description`)}
        footer={
          <>
            <button
              type="button"
              className={styles.footerButton}
              onClick={closeEditor}
              disabled={saving}
            >
              {t('providersPage.actions.cancel')}
            </button>
            <button
              type="submit"
              form="plugin-credential-form"
              className={`${styles.footerButton} ${styles.footerPrimary}`}
              disabled={saving || disabled}
            >
              {saving ? <IconLoader2 size={14} /> : null}
              {editor.index === null
                ? t('providersPage.actions.create')
                : t('providersPage.actions.save')}
            </button>
          </>
        }
      >
        <form id="plugin-credential-form" className={styles.form} onSubmit={saveEditor}>
          <label className={styles.field}>
            <span>{t('providersPage.form.apiKey')}</span>
            <input
              type="password"
              className={styles.input}
              value={editor.key}
              onChange={(event) => setEditor((state) => ({ ...state, key: event.target.value }))}
              placeholder={t('providersPage.form.apiKeyCreatePlaceholder')}
              autoComplete="off"
              disabled={saving}
              required
            />
          </label>
          {provider === 'commandcode' ? (
            <>
              <label className={styles.field}>
                <span>{t('providersPage.form.weight')}</span>
                <input
                  type="number"
                  min="1"
                  step="1"
                  className={styles.input}
                  value={editor.weight}
                  onChange={(event) =>
                    setEditor((state) => ({ ...state, weight: event.target.value }))
                  }
                  disabled={saving}
                  required
                />
                <small>{t('providersPage.form.weightHint')}</small>
              </label>
              <label className={styles.field}>
                <span>{t('providersPage.form.proxyUrl')}</span>
                <input
                  type="url"
                  className={styles.input}
                  value={editor.proxyUrl}
                  onChange={(event) =>
                    setEditor((state) => ({ ...state, proxyUrl: event.target.value }))
                  }
                  placeholder="http://127.0.0.1:18080"
                  autoComplete="off"
                  disabled={saving}
                />
              </label>
            </>
          ) : null}
        </form>
      </Sheet>
      <Sheet
        open={modelEditor.open}
        onClose={closeModelEditor}
        closeDisabled={saving}
        eyebrow={t('providersPage.integrations.credentials.modelsEyebrow')}
        title={`${t('providersPage.integrations.credentials.modelsTitle')} · ${providerName}`}
        description={t('providersPage.integrations.credentials.modelsHint')}
        footer={
          <>
            <button
              type="button"
              className={styles.footerButton}
              onClick={restoreDefaultModels}
              disabled={saving}
            >
              {t('providersPage.integrations.credentials.restoreModelDefaults')}
            </button>
            <button
              type="button"
              className={styles.footerButton}
              onClick={closeModelEditor}
              disabled={saving}
            >
              {t('providersPage.actions.cancel')}
            </button>
            <button
              type="submit"
              form="commandcode-model-form"
              className={`${styles.footerButton} ${styles.footerPrimary}`}
              disabled={saving || disabled}
            >
              {saving ? <IconLoader2 size={14} /> : null}
              {t('providersPage.actions.save')}
            </button>
          </>
        }
      >
        <form id="commandcode-model-form" className={styles.form} onSubmit={saveModels}>
          <div className={styles.modelRows}>
            {modelEditor.models.map((model, index) => (
              <div className={styles.modelRow} key={index}>
                <label className={styles.field}>
                  <span>{t('providersPage.integrations.credentials.modelAlias')}</span>
                  <input
                    className={styles.input}
                    value={model.alias}
                    onChange={(event) => updateModel(index, { alias: event.target.value })}
                    placeholder="deepseek-flash"
                    disabled={saving}
                    required={!modelEditor.usesDefaults}
                  />
                </label>
                <label className={styles.field}>
                  <span>{t('providersPage.integrations.credentials.upstreamModel')}</span>
                  <input
                    className={styles.input}
                    value={model.name}
                    onChange={(event) => updateModel(index, { name: event.target.value })}
                    placeholder="deepseek/deepseek-v4.1-flash"
                    disabled={saving}
                    required={!modelEditor.usesDefaults}
                  />
                </label>
                <label className={styles.field}>
                  <span>{t('providersPage.integrations.credentials.modelDisplayName')}</span>
                  <input
                    className={styles.input}
                    value={model.displayName}
                    onChange={(event) => updateModel(index, { displayName: event.target.value })}
                    placeholder="DeepSeek V4.1 Flash"
                    disabled={saving}
                  />
                </label>
                <button
                  type="button"
                  className={`${styles.iconButton} ${styles.deleteButton} ${styles.modelDelete}`}
                  onClick={() => removeModel(index)}
                  disabled={saving}
                  aria-label={t('providersPage.integrations.credentials.removeModel')}
                  title={t('providersPage.integrations.credentials.removeModel')}
                >
                  <IconTrash2 size={16} />
                </button>
              </div>
            ))}
          </div>
          <button
            type="button"
            className={styles.addModelButton}
            onClick={addModel}
            disabled={saving}
          >
            <IconPlus size={15} />
            {t('providersPage.integrations.credentials.addModel')}
          </button>
        </form>
      </Sheet>
      <Sheet
        open={policyEditor.open}
        onClose={closePolicyEditor}
        closeDisabled={saving}
        eyebrow={t('providersPage.integrations.modelPolicy.eyebrow')}
        title={`${t('providersPage.integrations.credentials.modelsTitle')} · ${providerName}`}
        description={t('providersPage.integrations.modelPolicy.description')}
        footer={
          <>
            <button
              type="button"
              className={styles.footerButton}
              onClick={closePolicyEditor}
              disabled={saving}
            >
              {t('providersPage.actions.cancel')}
            </button>
            <button
              type="submit"
              form="plugin-model-policy-form"
              className={`${styles.footerButton} ${styles.footerPrimary}`}
              disabled={
                saving ||
                disabled ||
                modelPolicySignature(policyEditor.policy) === modelPolicySignature(policy)
              }
            >
              {saving ? <IconLoader2 size={14} /> : null}
              {t('providersPage.actions.save')}
            </button>
          </>
        }
      >
        <form id="plugin-model-policy-form" onSubmit={savePolicy}>
          <ModelPolicyEditor
            value={policyEditor.policy}
            catalog={policyCatalog}
            catalogLoading={policyCatalogLoading}
            catalogError={policyCatalogError}
            disabled={saving}
            onChange={(nextPolicy) =>
              setPolicyEditor((state) => ({ ...state, policy: nextPolicy }))
            }
          />
        </form>
      </Sheet>
    </>
  );
}
