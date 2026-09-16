import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconPlus, IconTrash2 } from '@/components/ui/icons';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import type { OAuthModelAliasEntry } from '@/types';
import { normalizeModelNames, type ModelPolicy } from '../modelPolicy';
import styles from './ProviderPluginCredentialsPanel.module.scss';

interface ModelPolicyEditorProps {
  value: ModelPolicy;
  catalog: string[];
  catalogLoading?: boolean;
  catalogError?: string;
  disabled?: boolean;
  onChange: (policy: ModelPolicy) => void;
}

export function ModelPolicyEditor({
  value,
  catalog,
  catalogLoading = false,
  catalogError = '',
  disabled = false,
  onChange,
}: ModelPolicyEditorProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const options = useMemo(
    () => normalizeModelNames([...value.allowedModels, ...catalog]),
    [catalog, value.allowedModels]
  );
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return options;
    return options.filter((model) => model.toLowerCase().includes(needle));
  }, [options, query]);
  const exactMatch = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return Boolean(needle && options.some((model) => model.toLowerCase() === needle));
  }, [options, query]);

  const update = (patch: Partial<ModelPolicy>) => onChange({ ...value, ...patch });
  const toggleAllowed = (model: string) => {
    const selected = value.allowedModels.some(
      (item) => item.toLowerCase() === model.toLowerCase()
    );
    update({
      allowedModels: selected
        ? value.allowedModels.filter((item) => item.toLowerCase() !== model.toLowerCase())
        : normalizeModelNames([...value.allowedModels, model]),
    });
  };
  const addCustomAllowed = () => {
    const model = query.trim();
    if (!model || exactMatch) return;
    update({ allowedModels: normalizeModelNames([...value.allowedModels, model]) });
    setQuery('');
  };
  const updateAlias = (index: number, patch: Partial<OAuthModelAliasEntry>) => {
    update({
      aliases: value.aliases.map((entry, entryIndex) =>
        entryIndex === index ? { ...entry, ...patch } : entry
      ),
    });
  };

  return (
    <div className={styles.policyEditor}>
      <section className={styles.policyBlock}>
        <div className={styles.policyBlockHeader}>
          <div>
            <h3>{t('providersPage.integrations.modelPolicy.allowedTitle')}</h3>
            <p>{t('providersPage.integrations.modelPolicy.allowedHint')}</p>
          </div>
          <button
            type="button"
            className={styles.footerButton}
            onClick={() => update({ allowedModels: [] })}
            disabled={disabled || value.allowedModels.length === 0}
          >
            {t('providersPage.integrations.modelPolicy.unrestricted')}
          </button>
        </div>
        <div className={styles.policySearchRow}>
          <input
            className={styles.input}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter' && query.trim() && !exactMatch) {
                event.preventDefault();
                addCustomAllowed();
              }
            }}
            placeholder={t('providersPage.integrations.modelPolicy.searchPlaceholder')}
            disabled={disabled}
          />
          <button
            type="button"
            className={styles.footerButton}
            onClick={addCustomAllowed}
            disabled={disabled || !query.trim() || exactMatch}
          >
            {t('providersPage.integrations.modelPolicy.addCustom')}
          </button>
        </div>
        {value.allowedModels.length > 0 ? (
          <div className={styles.modelList}>
            {value.allowedModels.map((model) => (
              <button
                key={model}
                type="button"
                className={styles.policyTag}
                onClick={() => toggleAllowed(model)}
                disabled={disabled}
                title={t('providersPage.integrations.modelPolicy.removeAllowed', { model })}
              >
                {model} ×
              </button>
            ))}
          </div>
        ) : (
          <p className={styles.modelSource}>
            {t('providersPage.integrations.modelPolicy.unrestrictedHint')}
          </p>
        )}
        <div className={styles.policyOptions}>
          {catalogLoading && options.length === 0 ? (
            <div className={styles.policyMessage}>
              {t('providersPage.integrations.modelPolicy.loading')}
            </div>
          ) : null}
          {!catalogLoading && filtered.length === 0 ? (
            <div className={styles.policyMessage}>
              {t('providersPage.integrations.modelPolicy.empty')}
            </div>
          ) : null}
          {filtered.map((model) => (
            <SelectionCheckbox
              key={model}
              checked={value.allowedModels.some(
                (item) => item.toLowerCase() === model.toLowerCase()
              )}
              disabled={disabled}
              onChange={() => toggleAllowed(model)}
              className={styles.policyOption}
              label={<span title={model}>{model}</span>}
            />
          ))}
        </div>
        {catalogError ? (
          <p className={styles.policyError}>
            {t('providersPage.integrations.modelPolicy.catalogError')}
          </p>
        ) : null}
      </section>

      <section className={styles.policyBlock}>
        <div className={styles.policyBlockHeader}>
          <div>
            <h3>{t('providersPage.integrations.modelPolicy.aliasTitle')}</h3>
            <p>{t('providersPage.integrations.modelPolicy.aliasHint')}</p>
          </div>
        </div>
        <div className={styles.modelRows}>
          {value.aliases.map((entry, index) => (
            <div className={styles.policyAliasRow} key={index}>
              <label className={styles.field}>
                <span>{t('providersPage.integrations.credentials.modelAlias')}</span>
                <input
                  className={styles.input}
                  value={entry.alias}
                  onChange={(event) => updateAlias(index, { alias: event.target.value })}
                  placeholder="client-model"
                  disabled={disabled}
                />
              </label>
              <label className={styles.field}>
                <span>{t('providersPage.integrations.credentials.upstreamModel')}</span>
                <input
                  className={styles.input}
                  list="provider-model-policy-catalog"
                  value={entry.name}
                  onChange={(event) => updateAlias(index, { name: event.target.value })}
                  placeholder="upstream/model"
                  disabled={disabled}
                />
              </label>
              <label className={styles.policyCheck}>
                <input
                  type="checkbox"
                  checked={entry.fork === true}
                  onChange={(event) => updateAlias(index, { fork: event.target.checked })}
                  disabled={disabled}
                />
                <span>{t('providersPage.integrations.modelPolicy.keepOriginal')}</span>
              </label>
              <label className={styles.policyCheck}>
                <input
                  type="checkbox"
                  checked={entry.forceMapping === true}
                  onChange={(event) =>
                    updateAlias(index, { forceMapping: event.target.checked })
                  }
                  disabled={disabled}
                />
                <span>{t('providersPage.integrations.modelPolicy.forceMapping')}</span>
              </label>
              <button
                type="button"
                className={`${styles.iconButton} ${styles.deleteButton} ${styles.modelDelete}`}
                onClick={() =>
                  update({ aliases: value.aliases.filter((_, entryIndex) => entryIndex !== index) })
                }
                disabled={disabled}
                aria-label={t('providersPage.integrations.credentials.removeModel')}
              >
                <IconTrash2 size={16} />
              </button>
            </div>
          ))}
        </div>
        <button
          type="button"
          className={styles.addModelButton}
          onClick={() =>
            update({ aliases: [...value.aliases, { alias: '', name: '', fork: true }] })
          }
          disabled={disabled}
        >
          <IconPlus size={15} />
          {t('providersPage.integrations.modelPolicy.addAlias')}
        </button>
        <datalist id="provider-model-policy-catalog">
          {options.map((model) => (
            <option key={model} value={model} />
          ))}
        </datalist>
      </section>
    </div>
  );
}
