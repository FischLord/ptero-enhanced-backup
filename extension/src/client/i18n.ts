import { useCallback } from 'react';
import { useExtensionTranslation } from '@pterodactyl/sdk';
import en from './lang/en.json';

export type MessageKey = keyof typeof en;
export type Values = Record<string, string | number>;

const interpolate = (text: string, values?: Values): string =>
    values ? text.replace(/\{\{\s*(\w+)\s*\}\}/g, (_, name: string) => String(values[name] ?? '')) : text;

/**
 * Uebersetzungen aus resources/lang/<sprache>/messages.php (vom Panel geladen).
 * Fehlt ein Schluessel oder sind die Texte noch nicht da, gilt der englische Text aus lang/en.json.
 */
export function useT() {
    const { t: translate, ready, locale } = useExtensionTranslation('messages');
    const t = useCallback(
        (key: MessageKey, values?: Values): string => {
            if (ready) {
                const result = translate(key, values);
                if (result && result !== key) return result;
            }

            return interpolate(en[key], values);
        },
        [translate, ready],
    );

    return { t, locale };
}
