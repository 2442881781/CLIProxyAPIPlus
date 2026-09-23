import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { Sheet } from '@/components/ui/Sheet/Sheet';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { SEARCH_PROVIDERS, type SearchKeyEntry } from '@/services/api';
import { validateSearchKeyForm } from './searchKeysModel';
import styles from './SearchKeysPage.module.scss';

interface SearchKeyEditSheetProps {
  open: boolean;
  entry: SearchKeyEntry | null;
  editingIndex?: number;
  existing: SearchKeyEntry[];
  mutating: boolean;
  onClose: () => void;
  onSubmit: (entry: SearchKeyEntry) => Promise<void>;
}

const emptyEntry: SearchKeyEntry = {
  provider: 'tavily',
  apiKey: '',
  label: '',
  baseUrl: '',
  proxyUrl: '',
  disabled: false,
  budget: 0,
};

const providerOptions = SEARCH_PROVIDERS.map((provider) => ({ value: provider, label: provider }));

export function SearchKeyEditSheet({
  open,
  entry,
  editingIndex,
  existing,
  mutating,
  onClose,
  onSubmit,
}: SearchKeyEditSheetProps) {
  const { t } = useTranslation();
  const editing = entry !== null;
  const [form, setForm] = useState<SearchKeyEntry>(() => entry ?? emptyEntry);
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (open) {
      setForm(entry ?? emptyEntry);
      setTouched(false);
    }
  }, [open, entry]);

  const patch = (partial: Partial<SearchKeyEntry>) => setForm((prev) => ({ ...prev, ...partial }));
  const errorKey = validateSearchKeyForm(form, existing, editingIndex);

  const handleSubmit = async () => {
    setTouched(true);
    if (errorKey || mutating) return;
    await onSubmit({
      ...form,
      provider: form.provider.trim().toLowerCase(),
      apiKey: form.apiKey.trim(),
    });
  };

  return (
    <Sheet
      open={open}
      onClose={onClose}
      size="md"
      title={editing ? t('search_keys.edit_title') : t('search_keys.create_title')}
      description={t('search_keys.sheet_description')}
      closeDisabled={mutating}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={mutating}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void handleSubmit()} disabled={mutating}>
            {mutating ? t('common.loading') : t('common.save')}
          </Button>
        </>
      }
    >
      <div className={styles.sheetForm}>
        <div className={styles.field}>
          <label className={styles.label}>{t('search_keys.field_provider')}</label>
          <Select
            value={form.provider}
            options={providerOptions}
            onChange={(provider) => patch({ provider })}
            ariaLabel={t('search_keys.field_provider')}
            fullWidth
          />
        </div>
        <Input
          label={t('search_keys.field_api_key')}
          value={form.apiKey}
          onChange={(event) => patch({ apiKey: event.target.value })}
          placeholder="tvly-… / exa-… / fc-…"
          autoComplete="off"
          spellCheck={false}
          error={touched && errorKey ? t(errorKey) : undefined}
        />
        <Input
          label={t('search_keys.field_label')}
          value={form.label}
          onChange={(event) => patch({ label: event.target.value })}
          hint={t('search_keys.field_label_hint')}
        />
        <Input
          label={t('search_keys.field_base_url')}
          value={form.baseUrl}
          onChange={(event) => patch({ baseUrl: event.target.value })}
          placeholder="https://api.firecrawl.dev"
          hint={t('search_keys.field_base_url_hint')}
        />
        <Input
          label={t('search_keys.field_proxy_url')}
          value={form.proxyUrl}
          onChange={(event) => patch({ proxyUrl: event.target.value })}
          placeholder="socks5://127.0.0.1:1080"
          hint={t('search_keys.field_proxy_url_hint')}
        />
        {form.provider === 'exa' ? (
          <Input
            label={t('search_keys.field_budget')}
            type="number"
            min={0}
            step="0.01"
            value={form.budget > 0 ? String(form.budget) : ''}
            onChange={(event) => {
              const parsed = Number.parseFloat(event.target.value);
              patch({ budget: Number.isFinite(parsed) && parsed > 0 ? parsed : 0 });
            }}
            placeholder="10"
            hint={t('search_keys.field_budget_hint')}
          />
        ) : null}
        <ToggleSwitch
          checked={form.disabled}
          onChange={(disabled) => patch({ disabled })}
          label={t('search_keys.field_disabled')}
        />
      </div>
    </Sheet>
  );
}
