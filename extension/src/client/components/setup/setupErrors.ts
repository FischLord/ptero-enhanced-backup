import { errorInfo } from '../../api';
import type { MessageKey } from '../../i18n';

const KNOWN = ['wings_unreachable', 'allocation_unavailable', 'egg_missing', 'egg_outdated', 'host_required'] as const;

/** Text zu einem Fehler der Setup-Routen: bekannter Code uebersetzt, sonst erste Validierungsmeldung oder Servertext. */
export function setupErrorText(error: unknown, t: (key: MessageKey) => string, fallback: MessageKey): string {
    const info = errorInfo(error);
    if ((KNOWN as readonly string[]).includes(info.code)) return t(`setup_error_${info.code}` as MessageKey);

    // Laravel liefert {errors: {feld: [text]}}, das Panel {errors: [{detail}]}.
    const errors = (error as { response?: { data?: { errors?: unknown } } } | null)?.response?.data?.errors;
    const items: unknown[] = Array.isArray(errors) ? errors : errors && typeof errors === 'object' ? Object.values(errors).flat() : [];
    const first = items
        .map((item) => (typeof item === 'string' ? item : (item as { detail?: unknown } | null)?.detail))
        .find((v): v is string => typeof v === 'string');

    return first ?? t(fallback);
}
