import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Sheet } from '@/components/ui/Sheet/Sheet';
import type { AccessAuthItem, AccessGroup, RateLimitSpec } from '@/types';
import type { AccessGroupPayload } from '@/services/api/accessControl';
import { ModelAllowlistPicker } from './ModelAllowlistPicker';
import styles from './AccessGroupsPage.module.scss';

interface GroupEditSheetProps {
  open: boolean;
  group: AccessGroup | null;
  auths: AccessAuthItem[];
  models: string[];
  modelsLoading: boolean;
  modelsError: string;
  mutating: boolean;
  onClose: () => void;
  onSubmit: (payload: AccessGroupPayload) => Promise<void>;
}

interface FormState {
  name: string;
  allowedModels: string[];
  maxConcurrency: string;
  rateLimitRpm: string;
  perKeyRpm: string;
  perKeyTpm: string;
  perKeyRpd: string;
  perKeyConcurrency: string;
  allowedAuths: string[];
}

const emptyForm: FormState = {
  name: '',
  allowedModels: [],
  maxConcurrency: '',
  rateLimitRpm: '',
  perKeyRpm: '',
  perKeyTpm: '',
  perKeyRpd: '',
  perKeyConcurrency: '',
  allowedAuths: [],
};

const formFromGroup = (group: AccessGroup | null): FormState => {
  if (!group) return emptyForm;
  const pk = group.perKeyLimits;
  const num = (v: number) => (v > 0 ? String(v) : '');
  return {
    name: group.name,
    allowedModels: [...group.allowedModels],
    maxConcurrency: num(group.maxConcurrency),
    rateLimitRpm: num(group.rateLimitRpm),
    perKeyRpm: num(pk.rpm),
    perKeyTpm: num(pk.tpm),
    perKeyRpd: num(pk.rpd),
    perKeyConcurrency: num(pk.maxConcurrency),
    allowedAuths: [...group.allowedAuths],
  };
};

const toInt = (value: string): number => {
  const parsed = Number.parseInt(value.trim(), 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
};

export function GroupEditSheet({
  open,
  group,
  auths,
  models,
  modelsLoading,
  modelsError,
  mutating,
  onClose,
  onSubmit,
}: GroupEditSheetProps) {
  const { t } = useTranslation();
  const editing = group !== null;
  const [form, setForm] = useState<FormState>(() => formFromGroup(group));

  useEffect(() => {
    if (open) setForm(formFromGroup(group));
  }, [open, group]);

  const authLabel = useMemo(() => {
    const map = new Map<string, string>();
    for (const auth of auths) {
      map.set(auth.id, auth.label || auth.id);
    }
    return map;
  }, [auths]);

  const patch = (partial: Partial<FormState>) => setForm((prev) => ({ ...prev, ...partial }));

  const toggleAuth = (id: string) =>
    patch({
      allowedAuths: form.allowedAuths.includes(id)
        ? form.allowedAuths.filter((item) => item !== id)
        : [...form.allowedAuths, id],
    });

  const valid = form.name.trim().length > 0;

  const handleSubmit = async () => {
    if (!valid || mutating) return;
    const perKeyLimits: RateLimitSpec = {
      rpm: toInt(form.perKeyRpm),
      tpm: toInt(form.perKeyTpm),
      rpd: toInt(form.perKeyRpd),
      maxConcurrency: toInt(form.perKeyConcurrency),
    };
    await onSubmit({
      name: form.name,
      allowedAuths: form.allowedAuths,
      allowedModels: form.allowedModels,
      maxConcurrency: toInt(form.maxConcurrency),
      rateLimitRpm: toInt(form.rateLimitRpm),
      perKeyLimits,
    });
  };

  const numberField = (key: keyof FormState, label: string, hint?: string) => (
    <div className={styles.field} key={key}>
      <label className={styles.label}>{label}</label>
      <Input
        type="number"
        min={0}
        value={form[key] as string}
        onChange={(event) => patch({ [key]: event.target.value })}
        placeholder="0"
      />
      {hint ? <span className={styles.hint}>{hint}</span> : null}
    </div>
  );

  return (
    <Sheet
      open={open}
      onClose={onClose}
      size="lg"
      title={editing ? t('access_groups.edit_title') : t('access_groups.create_title')}
      description={t('access_groups.sheet_description')}
      closeDisabled={mutating}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={mutating}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void handleSubmit()} disabled={!valid || mutating}>
            {mutating ? t('common.loading') : t('common.save')}
          </Button>
        </>
      }
    >
      <div className={styles.sheetForm}>
        <div className={styles.field}>
          <label className={styles.label}>{t('access_groups.field_name')}</label>
          <Input
            value={form.name}
            onChange={(event) => patch({ name: event.target.value })}
            placeholder="infra"
            disabled={editing}
          />
          {editing ? (
            <span className={styles.hint}>{t('access_groups.name_immutable')}</span>
          ) : null}
        </div>

        <div className={styles.field}>
          <label className={styles.label}>{t('access_groups.field_models')}</label>
          <ModelAllowlistPicker
            options={models}
            value={form.allowedModels}
            loading={modelsLoading}
            error={modelsError}
            disabled={mutating}
            onChange={(allowedModels) => patch({ allowedModels })}
          />
          <span className={styles.hint}>{t('access_groups.field_models_hint')}</span>
        </div>

        <div className={styles.gridTwo}>
          {numberField('maxConcurrency', t('access_groups.field_concurrency'))}
          {numberField('rateLimitRpm', t('access_groups.field_group_rpm'))}
        </div>

        <fieldset className={styles.fieldset}>
          <legend className={styles.label}>{t('access_groups.field_per_key')}</legend>
          <span className={styles.hint}>{t('access_groups.field_per_key_hint')}</span>
          <div className={styles.gridTwo}>
            {numberField('perKeyRpm', t('access_groups.field_rpm'))}
            {numberField('perKeyTpm', t('access_groups.field_tpm'))}
            {numberField('perKeyRpd', t('access_groups.field_rpd'))}
            {numberField('perKeyConcurrency', t('access_groups.field_key_concurrency'))}
          </div>
        </fieldset>

        <fieldset className={styles.fieldset}>
          <legend className={styles.label}>{t('access_groups.field_auths')}</legend>
          <span className={styles.hint}>{t('access_groups.field_auths_hint')}</span>
          {auths.length === 0 ? (
            <span className={styles.hint}>{t('access_groups.no_auths')}</span>
          ) : (
            <div className={styles.authGrid}>
              {auths.map((auth) => {
                const checked = form.allowedAuths.includes(auth.id);
                return (
                  <label key={auth.id} className={styles.authItem}>
                    <input
                      type="checkbox"
                      checked={checked}
                      onChange={() => toggleAuth(auth.id)}
                      disabled={auth.disabled}
                    />
                    <span className={styles.authName}>
                      {authLabel.get(auth.id) ?? auth.id}
                      {auth.disabled ? ` (${t('access_groups.auth_disabled')})` : ''}
                    </span>
                    <span className={styles.authMeta}>{auth.provider}</span>
                  </label>
                );
              })}
            </div>
          )}
        </fieldset>
      </div>
    </Sheet>
  );
}
