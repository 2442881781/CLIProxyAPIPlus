import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconChevronDown, IconChevronUp } from '@/components/ui/icons';
import { SEARCH_PROVIDERS, searchKeysApi, type SearchMcpSettings } from '@/services/api';
import { useNotificationStore } from '@/stores';
import { moveProvider, toggleProvider } from './searchKeysModel';
import styles from './SearchKeysPage.module.scss';

interface SearchMcpSettingsCardProps {
  connected: boolean;
}

export function SearchMcpSettingsCard({ connected }: SearchMcpSettingsCardProps) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [saved, setSaved] = useState<SearchMcpSettings | null>(null);
  const [draft, setDraft] = useState<SearchMcpSettings | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      const settings = await searchKeysApi.mcpSettings();
      setSaved(settings);
      setDraft(settings);
    } catch (loadError: unknown) {
      setError(loadError instanceof Error ? loadError.message : t('common.unknown_error'));
    }
  }, [t]);

  useEffect(() => {
    if (connected) void load();
  }, [connected, load]);

  if (!draft) return null;

  const dirty = JSON.stringify(draft) !== JSON.stringify(saved);
  const disabledProviders = SEARCH_PROVIDERS.filter((p) => !draft.providerOrder.includes(p));

  const save = async () => {
    setSaving(true);
    setError('');
    try {
      await searchKeysApi.saveMcpSettings(draft);
      setSaved(draft);
      showNotification(t('search_keys.mcp_saved'), 'success');
    } catch (saveError: unknown) {
      setError(saveError instanceof Error ? saveError.message : t('common.unknown_error'));
    } finally {
      setSaving(false);
    }
  };

  const providerRow = (provider: string, enabled: boolean, index: number) => (
    <div key={provider} className={styles.mcpProvider} data-enabled={enabled}>
      <span className={styles.mcpRank}>{enabled ? index + 1 : '–'}</span>
      <code>{provider}</code>
      <ToggleSwitch
        checked={enabled}
        onChange={() =>
          setDraft({ ...draft, providerOrder: toggleProvider(draft.providerOrder, provider) })
        }
        ariaLabel={`${provider} ${t('search_keys.provider_enabled')}`}
        disabled={enabled && draft.providerOrder.length === 1}
      />
      <div className={styles.mcpMove}>
        <button
          type="button"
          aria-label={t('search_keys.move_up')}
          disabled={!enabled || index === 0}
          onClick={() =>
            setDraft({ ...draft, providerOrder: moveProvider(draft.providerOrder, provider, -1) })
          }
        >
          <IconChevronUp size={14} />
        </button>
        <button
          type="button"
          aria-label={t('search_keys.move_down')}
          disabled={!enabled || index === draft.providerOrder.length - 1}
          onClick={() =>
            setDraft({ ...draft, providerOrder: moveProvider(draft.providerOrder, provider, 1) })
          }
        >
          <IconChevronDown size={14} />
        </button>
      </div>
    </div>
  );

  return (
    <section className={styles.usageCard}>
      <h2>{t('search_keys.mcp_title')}</h2>
      <p className={styles.hint}>{t('search_keys.mcp_description')}</p>
      {error && (
        <div className={styles.error} role="alert">
          {error}
        </div>
      )}
      <div className={styles.usageBlock}>
        <div className={styles.label}>{t('search_keys.mcp_order')}</div>
        {draft.providerOrder.map((provider, index) => providerRow(provider, true, index))}
        {disabledProviders.map((provider) => providerRow(provider, false, -1))}
      </div>
      <div className={styles.usageBlock}>
        <ToggleSwitch
          checked={draft.exposeProviderTools}
          onChange={(exposeProviderTools) => setDraft({ ...draft, exposeProviderTools })}
          label={t('search_keys.mcp_expose_tools')}
        />
        <span className={styles.hint}>{t('search_keys.mcp_expose_tools_hint')}</span>
      </div>
      <div>
        <Button size="sm" onClick={() => void save()} disabled={!dirty || saving || !connected}>
          {saving ? t('common.loading') : t('common.save')}
        </Button>
      </div>
    </section>
  );
}
