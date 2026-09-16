import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { IconChevronDown, IconSearch, IconX } from '@/components/ui/icons';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { filterModelNames, mergeModelNames } from './modelAllowlist';
import styles from './AccessGroupsPage.module.scss';

interface ModelAllowlistPickerProps {
  options: string[];
  value: string[];
  loading: boolean;
  error: string;
  disabled?: boolean;
  onChange: (models: string[]) => void;
}

export function ModelAllowlistPicker({
  options,
  value,
  loading,
  error,
  disabled = false,
  onChange,
}: ModelAllowlistPickerProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const rootRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const allModels = useMemo(() => mergeModelNames(value, options), [options, value]);
  const filtered = useMemo(() => filterModelNames(allModels, query), [allModels, query]);
  const exactMatch = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return Boolean(needle && allModels.some((model) => model.toLowerCase() === needle));
  }, [allModels, query]);

  useEffect(() => {
    if (!open) return;
    const handleOutside = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) {
        setOpen(false);
        setQuery('');
      }
    };
    document.addEventListener('pointerdown', handleOutside);
    return () => document.removeEventListener('pointerdown', handleOutside);
  }, [open]);

  useEffect(() => {
    if (open) inputRef.current?.focus();
  }, [open]);

  const toggle = (model: string) => {
    const selected = value.some((item) => item.toLowerCase() === model.toLowerCase());
    onChange(
      selected
        ? value.filter((item) => item.toLowerCase() !== model.toLowerCase())
        : [...value, model]
    );
  };

  const addCustom = () => {
    const custom = query.trim();
    if (!custom || exactMatch) return;
    onChange(mergeModelNames(value, [custom]));
    setQuery('');
  };

  const handleInputKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      if (!exactMatch && query.trim()) addCustom();
      else if (filtered.length === 1) toggle(filtered[0]);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      setOpen(false);
      setQuery('');
    }
  };

  return (
    <div className={styles.modelPicker} ref={rootRef}>
      <button
        type="button"
        className={styles.modelPickerTrigger}
        onClick={() => setOpen((current) => !current)}
        disabled={disabled}
        aria-expanded={open}
      >
        <span className={value.length ? styles.modelPickerValue : styles.modelPickerPlaceholder}>
          {value.length
            ? t('access_groups.models_selected', { count: value.length })
            : t('access_groups.field_models_placeholder')}
        </span>
        <IconChevronDown size={16} />
      </button>

      {value.length > 0 ? (
        <div className={styles.modelTags}>
          {value.map((model) => (
            <span className={styles.modelTag} key={model}>
              <span title={model}>{model}</span>
              <button
                type="button"
                onClick={() => toggle(model)}
                disabled={disabled}
                aria-label={t('access_groups.model_remove', { model })}
              >
                <IconX size={12} />
              </button>
            </span>
          ))}
          <button
            type="button"
            className={styles.modelClear}
            onClick={() => onChange([])}
            disabled={disabled}
          >
            {t('access_groups.models_clear')}
          </button>
        </div>
      ) : null}

      {open ? (
        <div className={styles.modelPickerPanel}>
          <div className={styles.modelSearch}>
            <IconSearch size={16} />
            <input
              ref={inputRef}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={handleInputKeyDown}
              placeholder={t('access_groups.models_search_placeholder')}
              autoComplete="off"
            />
          </div>

          <div className={styles.modelPickerToolbar}>
            <span>{t('access_groups.models_selected', { count: value.length })}</span>
            <div>
              <button type="button" onClick={() => onChange(mergeModelNames(value, filtered))}>
                {t('access_groups.models_select_visible')}
              </button>
              <button type="button" onClick={() => onChange([])} disabled={value.length === 0}>
                {t('access_groups.models_clear')}
              </button>
            </div>
          </div>

          <div className={styles.modelOptions}>
            {loading && allModels.length === 0 ? (
              <div className={styles.modelPickerMessage}>{t('access_groups.models_loading')}</div>
            ) : null}
            {!loading && filtered.length === 0 && !query.trim() ? (
              <div className={styles.modelPickerMessage}>{t('access_groups.models_empty')}</div>
            ) : null}
            {filtered.map((model) => (
              <SelectionCheckbox
                key={model}
                className={styles.modelOption}
                checked={value.some((item) => item.toLowerCase() === model.toLowerCase())}
                onChange={() => toggle(model)}
                label={<span title={model}>{model}</span>}
              />
            ))}
            {query.trim() && !exactMatch ? (
              <button type="button" className={styles.modelCustomOption} onClick={addCustom}>
                {t('access_groups.models_add_custom', { model: query.trim() })}
              </button>
            ) : null}
          </div>
          {error ? (
            <div className={styles.modelPickerError}>{t('access_groups.models_load_failed')}</div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
