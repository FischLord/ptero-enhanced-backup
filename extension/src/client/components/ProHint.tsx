import { useQuery } from '@tanstack/react-query';
import { Badge } from '@pterodactyl/sdk';
import { getEdition, keys } from '../api';
import { useT, type MessageKey } from '../i18n';

/**
 * Kleiner Hinweis auf eine Pro-Funktion, nur dort eingesetzt, wo sie erscheinen wuerde.
 * Er verschwindet, sobald die Pro-Extension aktiv ist (oder solange unklar ist, ob sie es ist,
 * damit er nie kurz aufblitzt).
 */
export default function ProHint({ text }: { text: MessageKey }) {
    const { t } = useT();
    const query = useQuery({ queryKey: keys.edition(), queryFn: getEdition, retry: false, staleTime: 5 * 60 * 1000 });
    if (!query.data || query.data.pro.installed) return null;

    return (
        <p className={'peb:flex peb:items-center peb:gap-2 peb:text-xs peb:text-muted-foreground'}>
            <Badge tone={'accent'} variant={'soft'}>
                {t('pro_label')}
            </Badge>
            <span>{t(text)}</span>
        </p>
    );
}
