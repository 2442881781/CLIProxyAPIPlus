import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconExternalLink } from '@/components/ui/icons';
import styles from './AccessKeysPage.module.scss';

const ACCESS_KEYS_URL = '/access-keys.html';

export function AccessKeysPage() {
  const { t } = useTranslation();

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <div className={styles.eyebrow}>{t('access_keys.eyebrow')}</div>
          <h1>{t('access_keys.title')}</h1>
          <p>{t('access_keys.description')}</p>
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => window.open(ACCESS_KEYS_URL, '_blank', 'noopener')}
        >
          <span className={styles.buttonContent}>
            <IconExternalLink size={15} />
            {t('access_keys.open_full_page')}
          </span>
        </Button>
      </header>
      <iframe
        src={ACCESS_KEYS_URL}
        title={t('access_keys.title')}
        className={styles.frame}
      />
    </div>
  );
}
