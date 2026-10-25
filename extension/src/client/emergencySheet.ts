import type { MessageKey, Values } from './i18n';
import type { EmergencySheetData } from './types';

type Translate = (key: MessageKey, values?: Values) => string;

const escapeHtml = (text: string): string =>
    text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;');

/** Repository-Adresse in restic-Schreibweise: lokale Pfade ohne Praefix, alle anderen mit `typ:`. */
export function repoAddress(type: string, location: string): string {
    return type === 'local' || type === '' || location.startsWith(`${type}:`) ? location : `${type}:${location}`;
}

const STYLE = `
body{font-family:system-ui,-apple-system,"Segoe UI",sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;color:#111;line-height:1.45}
h1{font-size:1.6rem;margin:0 0 .25rem}h2{font-size:1.1rem;margin:1.6rem 0 .4rem;border-bottom:1px solid #999;padding-bottom:.2rem}
pre,code{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:.85rem}
pre{background:#f1f1f1;border:1px solid #ccc;border-radius:4px;padding:.5rem .7rem;white-space:pre-wrap;word-break:break-all}
.note{font-size:.85rem;color:#444}.box{border:2px solid #111;min-height:3.2rem;margin:.4rem 0}
table{border-collapse:collapse;width:100%;font-size:.85rem}td,th{border-bottom:1px solid #ccc;padding:.25rem .4rem;text-align:left;vertical-align:top}
.print{font-size:.8rem;color:#555}@media print{.print{display:none}body{margin:0}}`;

/**
 * Baut das druckbare Notfallblatt als HTML-Dokument. Es enthaelt nie das Restic-Passwort,
 * nur ein leeres Feld zum handschriftlichen Eintragen. Alle Werte werden maskiert.
 */
export function buildEmergencySheet(data: EmergencySheetData, t: Translate, lang: string): string {
    const e = escapeHtml;
    const repo = repoAddress(data.repo.type, data.repo.location);
    const sec = data.secondary ? repoAddress(data.secondary.type, data.secondary.repo) : null;
    const rclone = data.repo.type === 'rclone' || data.secondary?.type === 'rclone';
    const firstUuid = data.servers[0]?.uuid ?? '<server-uuid>';
    const cmd = (title: MessageKey, line: string) => `<p>${e(t(title))}</p><pre>${e(line)}</pre>`;
    const repoArg = (r: string) => (/[\s'"$\\]/.test(r) ? `'${r.replace(/'/g, `'\\''`)}'` : r);

    // Das Gateway sichert den Stream als Datei `/<uuid>.tar`; `dump` muss genau diesen Pfad nennen.
    const tarPath = `${firstUuid}.tar`;
    const restore = (r: string) =>
        [
            cmd('sheet_restore_step_env', 'export RESTIC_PASSWORD=...'),
            cmd('sheet_restore_step_list', `restic -r ${repoArg(r)} snapshots --tag server:${firstUuid}`),
            cmd('sheet_restore_step_dump', `restic -r ${repoArg(r)} dump <snapshot> /${tarPath} > ${tarPath}`),
            cmd('sheet_restore_step_restore', `restic -r ${repoArg(r)} restore <snapshot> --target ./restored`),
        ].join('');

    const servers =
        data.servers.length === 0
            ? `<p>${e(t('sheet_servers_empty'))}</p>`
            : `<table><tbody>${data.servers
                  .map((s) => `<tr><td>${s.name ? e(s.name) : `<em>${e(t('sheet_server_unknown'))}</em>`}</td><td><code>${e(s.uuid)}</code></td></tr>`)
                  .join('')}</tbody></table>`;

    return `<!doctype html>
<html lang="${e(lang)}"><head><meta charset="utf-8"><meta name="robots" content="noindex"><title>${e(t('sheet_title'))}</title><style>${STYLE}</style></head>
<body>
<h1>${e(t('sheet_title'))}</h1>
<p class="note">${e(t('sheet_created'))}: ${e(data.generated_at)}</p>
<p>${e(t('sheet_intro'))}</p>
<h2>${e(t('sheet_password'))}</h2>
<div class="box"></div>
<p class="note">${e(t('sheet_password_hint'))}</p>
<h2>${e(t('sheet_repo'))}</h2>
<pre>${e(repo)}</pre>
<p class="note">${e(t('sheet_repo_note'))}</p>
<h2>${e(t('sheet_offsite'))}</h2>
${sec ? `<pre>${e(sec)}</pre>` : `<p>${e(t('sheet_offsite_none'))}</p>`}
${rclone ? `<p class="note">${e(t('sheet_offsite_rclone'))}</p>` : ''}
<h2>${e(t('sheet_gateway'))}</h2>
<pre>${e(data.gateway_url)}</pre>
<h2>${e(t('sheet_restore_title'))}</h2>
${restore(repo)}
<p class="note">${e(t('sheet_restore_note'))}</p>
<h2>${e(t('sheet_servers'))}</h2>
${servers}
<p class="print">${e(t('sheet_print_hint'))}</p>
</body></html>`;
}
