import { Button, CopyOnClick } from '@pterodactyl/sdk';
import { useT } from '../../i18n';

/** Textblock mit Kopieren-Knopf. */
export default function SnippetBlock({ title, text }: { title: string; text: string }) {
    const { t } = useT();

    return (
        <div className={'peb:flex peb:flex-col peb:gap-1'}>
            <div className={'peb:flex peb:items-center peb:justify-between peb:gap-2'}>
                <h3 className={'peb:text-sm peb:font-medium'}>{title}</h3>
                <CopyOnClick text={text}>
                    <Button size={'xsmall'} isSecondary>
                        {t('setup_copy')}
                    </Button>
                </CopyOnClick>
            </div>
            <pre aria-label={title} className={'peb:overflow-x-auto peb:rounded-md peb:border peb:border-border peb:bg-muted peb:p-3 peb:font-mono peb:text-xs'}>
                {text}
            </pre>
        </div>
    );
}
