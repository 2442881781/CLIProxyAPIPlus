import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import commandCodeLogo from '@/assets/icons/commandcode.png';
import devinLogo from '@/assets/icons/devin.svg';
import openCodeLogo from '@/assets/icons/opencode.png';
import zhipuLogo from '@/assets/icons/zhipu.png';
import { authFilesApi, pluginsApi } from '@/services/api';
import { useAuthStore } from '@/stores';
import type { AuthFileItem, PluginConfigObject, PluginListEntry } from '@/types';
import { PROVIDER_LOGOS } from '../brandLogos';
import type { ProviderBrand, ProviderGroup } from '../types';
import { isZhipuCodingPlanResource } from '../zhipu';
import styles from './ProviderCategoryList.module.scss';

export type ProviderIntegrationID = 'commandcode' | 'opencode' | 'zhipu' | 'devin';

interface ProviderCategoryListProps {
  groups: ProviderGroup[];
  activeBrand: ProviderBrand;
  activeIntegration: ProviderIntegrationID | null;
  refreshKey?: string;
  onSelect: (brand: ProviderBrand) => void;
  onSelectIntegration: (id: ProviderIntegrationID) => void;
}

interface IntegrationCategory {
  id: ProviderIntegrationID;
  logo: string;
  total: number;
  activeCount: number;
}

const QUICK_FILL_BRAND_ORDER: readonly ProviderBrand[] = ['fennoAI', 'qiniuCloud'];

const QUICK_FILL_BRANDS: ReadonlySet<ProviderBrand> = new Set(QUICK_FILL_BRAND_ORDER);

const isDevinCredential = (file: AuthFileItem): boolean =>
  [file.type, file.provider].some(
    (value) =>
      String(value ?? '')
        .trim()
        .toLowerCase() === 'devin'
  );

const isActiveAuthFile = (file: AuthFileItem): boolean =>
  file.disabled !== true && file.unavailable !== true && file.status !== 'disabled';

export function ProviderCategoryList({
  groups,
  activeBrand,
  activeIntegration,
  refreshKey,
  onSelect,
  onSelectIntegration,
}: ProviderCategoryListProps) {
  const { t } = useTranslation();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const [plugins, setPlugins] = useState<PluginListEntry[]>([]);
  const [pluginConfigs, setPluginConfigs] = useState<Record<string, PluginConfigObject>>({});
  const [authFiles, setAuthFiles] = useState<AuthFileItem[]>([]);

  useEffect(() => {
    let active = true;
    if (!connected) return () => void (active = false);

    void Promise.allSettled([
      pluginsApi.list(),
      pluginsApi.getConfig('commandcode'),
      pluginsApi.getConfig('opencode-go-cliproxyapi'),
      authFilesApi.list(),
    ]).then(([pluginResult, commandCodeConfigResult, openCodeConfigResult, authResult]) => {
      if (!active) return;
      if (pluginResult.status === 'fulfilled') setPlugins(pluginResult.value.plugins);
      setPluginConfigs({
        ...(commandCodeConfigResult.status === 'fulfilled'
          ? { commandcode: commandCodeConfigResult.value }
          : {}),
        ...(openCodeConfigResult.status === 'fulfilled'
          ? { opencode: openCodeConfigResult.value }
          : {}),
      });
      if (authResult.status === 'fulfilled') setAuthFiles(authResult.value.files ?? []);
    });

    return () => {
      active = false;
    };
  }, [connected, refreshKey]);

  const quickFillGroups = groups
    .filter((g) => QUICK_FILL_BRANDS.has(g.id))
    .sort(
      (left, right) =>
        QUICK_FILL_BRAND_ORDER.indexOf(left.id) - QUICK_FILL_BRAND_ORDER.indexOf(right.id)
    );
  const providerGroups = groups.filter((g) => !QUICK_FILL_BRANDS.has(g.id));

  const integrationCategories = useMemo<IntegrationCategory[]>(() => {
    const commandCode = plugins.find((plugin) => plugin.id === 'commandcode');
    const openCode = plugins.find((plugin) => plugin.id === 'opencode-go-cliproxyapi');
    const commandCodeConfig = pluginConfigs.commandcode ?? {};
    const commandCodeKeys = Array.isArray(commandCodeConfig.api_keys)
      ? commandCodeConfig.api_keys.length
      : typeof commandCodeConfig.api_key === 'string' && commandCodeConfig.api_key.trim()
        ? 1
        : 0;
    const openCodeConfig = pluginConfigs.opencode ?? {};
    const openCodeKeys = Array.isArray(openCodeConfig['api-keys'])
      ? openCodeConfig['api-keys'].length
      : 0;
    const zhipuResources =
      groups
        .find((group) => group.id === 'openaiCompatibility')
        ?.resources.filter(isZhipuCodingPlanResource) ?? [];
    const devinFiles = authFiles.filter(isDevinCredential);

    return [
      {
        id: 'commandcode',
        logo: commandCodeLogo,
        total: commandCodeKeys,
        activeCount: commandCode?.registered && commandCode.effectiveEnabled ? commandCodeKeys : 0,
      },
      {
        id: 'opencode',
        logo: openCodeLogo,
        total: openCodeKeys,
        activeCount: openCode?.registered && openCode.effectiveEnabled ? openCodeKeys : 0,
      },
      {
        id: 'zhipu',
        logo: zhipuLogo,
        total: zhipuResources.reduce((count, resource) => count + resource.apiKeyEntryCount, 0),
        activeCount: zhipuResources
          .filter((resource) => !resource.disabled)
          .reduce((count, resource) => count + resource.apiKeyEntryCount, 0),
      },
      {
        id: 'devin',
        logo: devinLogo,
        total: devinFiles.length,
        activeCount: devinFiles.filter(isActiveAuthFile).length,
      },
    ];
  }, [authFiles, groups, pluginConfigs, plugins]);

  const renderGroups = (items: ProviderGroup[]) => (
    <div className={styles.list}>
      {items.map((group) => {
        const active = group.id === activeBrand;
        const total = group.resources.length;
        const activeCount = group.resources.filter((r) => !r.disabled).length;
        const logo = PROVIDER_LOGOS[group.id];
        const itemClass = [
          styles.item,
          active ? styles.active : '',
          group.id === 'kimi' ? styles.itemKimi : '',
        ]
          .filter(Boolean)
          .join(' ');
        const logoClassName = [
          styles.logo,
          logo?.transparent ? styles.logoTransparent : '',
          logo?.themeSurface ? styles.logoThemeSurface : '',
          logo?.darkSrc ? styles.logoThemeLight : '',
          logo?.invertOnDark ? styles.logoInvertOnDark : '',
        ]
          .filter(Boolean)
          .join(' ');
        const darkLogoClassName = [
          styles.logo,
          logo?.transparent ? styles.logoTransparent : '',
          logo?.themeSurface ? styles.logoThemeSurface : '',
          styles.logoThemeDark,
        ]
          .filter(Boolean)
          .join(' ');

        return (
          <button
            key={group.id}
            type="button"
            className={itemClass}
            onClick={() => onSelect(group.id)}
            aria-current={active ? 'page' : undefined}
          >
            <span className={styles.itemLeft}>
              {logo ? (
                <>
                  <img src={logo.src} alt="" aria-hidden="true" className={logoClassName} />
                  {logo.darkSrc ? (
                    <img
                      src={logo.darkSrc}
                      alt=""
                      aria-hidden="true"
                      className={darkLogoClassName}
                    />
                  ) : null}
                </>
              ) : null}
              <span className={styles.itemText}>
                <span className={styles.itemTitle}>
                  {t(`providersPage.providerNames.${group.id}`)}
                </span>
                <span className={styles.itemSubtitle}>
                  {t('providersPage.categories.activeCount', {
                    active: activeCount,
                    total,
                  })}
                </span>
              </span>
            </span>
            <span
              className={[
                styles.badge,
                total === 0 ? (group.id === 'kimi' ? styles.badgeKimi : styles.badgeAmber) : '',
              ]
                .filter(Boolean)
                .join(' ')}
            >
              {total}
            </span>
          </button>
        );
      })}
    </div>
  );

  const renderIntegrations = () => (
    <div className={styles.list}>
      {integrationCategories.map((item) => {
        const active = item.id === activeIntegration;
        return (
          <button
            key={item.id}
            type="button"
            className={[styles.item, active ? styles.active : ''].filter(Boolean).join(' ')}
            onClick={() => onSelectIntegration(item.id)}
            aria-current={active ? 'page' : undefined}
          >
            <span className={styles.itemLeft}>
              <img
                src={item.logo}
                alt=""
                aria-hidden="true"
                className={[styles.logo, styles.logoTransparent].join(' ')}
              />
              <span className={styles.itemText}>
                <span className={styles.itemTitle}>
                  {t(`providersPage.providerNames.${item.id}`)}
                </span>
                <span className={styles.itemSubtitle}>
                  {t('providersPage.categories.activeCount', {
                    active: item.activeCount,
                    total: item.total,
                  })}
                </span>
              </span>
            </span>
            <span className={[styles.badge, item.total === 0 ? styles.badgeAmber : ''].join(' ')}>
              {item.total}
            </span>
          </button>
        );
      })}
    </div>
  );

  return (
    <div className={styles.stack}>
      <aside className={styles.aside}>
        <p className={styles.eyebrow}>{t('providersPage.categories.title')}</p>
        {renderGroups(providerGroups.slice(0, 1))}
        {renderIntegrations()}
        {renderGroups(providerGroups.slice(1))}
      </aside>
      {quickFillGroups.length > 0 && (
        <aside className={styles.aside}>
          <p className={styles.eyebrow}>{t('providersPage.categories.quickFill')}</p>
          {renderGroups(quickFillGroups)}
        </aside>
      )}
    </div>
  );
}
