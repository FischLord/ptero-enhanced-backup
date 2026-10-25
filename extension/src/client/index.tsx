import './styles.css';
import { definePterodactylExtension } from '@pterodactyl/sdk';

export default definePterodactylExtension({
    setup({ screens }) {
        // IDs und Pfade stehen in extension.json (ui.screens).
        screens.register('backups', () => import('./screens/BackupsScreen'));
        screens.register('setup', () => import('./screens/SetupScreen'));
        screens.register('server-backups', () => import('./screens/ServerTabScreen'));
    },
});
