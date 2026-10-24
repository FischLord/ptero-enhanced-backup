<?php

// Bootstrap fuer die PHP-Unit-Tests der Extension. Laeuft mit dem Autoloader eines
// Panel-Checkouts (siehe Makefile: make test-php), ohne Datenbank und ohne gebootete App.
$panel = getenv('PANEL_ROOT') ?: dirname(__DIR__, 3);
$loader = require $panel.'/vendor/autoload.php';
$loader->addPsr4('PebBackups\\', dirname(__DIR__).'/src');
