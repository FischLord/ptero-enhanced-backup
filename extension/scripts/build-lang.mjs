// Erzeugt resources/lang/<locale>/messages.php aus src/client/lang/<locale>.json.
// Die JSON-Dateien sind die einzige Quelle: Das Frontend nutzt en.json als Rueckfall,
// das Panel liest die PHP-Dateien (loadExtensionTranslations).
// Prueft ausserdem, dass jede Sprache genau die Schluessel von en.json hat.
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const langDir = join(root, 'src/client/lang');
const en = JSON.parse(readFileSync(join(langDir, 'en.json'), 'utf8'));
const php = (s) => `'${String(s).replace(/\\/g, '\\\\').replace(/'/g, "\\'")}'`;
let failed = false;

for (const file of readdirSync(langDir).filter((f) => f.endsWith('.json'))) {
    const locale = file.replace(/\.json$/, '');
    const messages = JSON.parse(readFileSync(join(langDir, file), 'utf8'));
    const missing = Object.keys(en).filter((k) => !(k in messages));
    const extra = Object.keys(messages).filter((k) => !(k in en));
    if (missing.length || extra.length) {
        failed = true;
        console.error(`lang/${file}: fehlende Schluessel [${missing.join(', ')}], ueberzaehlige [${extra.join(', ')}]`);
    }
    const body = Object.entries(messages)
        .map(([k, v]) => `    ${php(k)} => ${php(v)},`)
        .join('\n');
    const out = join(root, 'resources/lang', locale);
    mkdirSync(out, { recursive: true });
    writeFileSync(join(out, 'messages.php'), `<?php\n\n// Erzeugt aus src/client/lang/${file} (npm run lang). Nicht von Hand aendern.\nreturn [\n${body}\n];\n`);
}

if (failed) process.exit(1);
