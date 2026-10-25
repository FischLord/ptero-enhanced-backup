<?php

namespace PebBackups\Setup;

use RuntimeException;

/** Fehler im Einrichtungsassistenten mit stabilem Code (die Oberflaeche uebersetzt ihn). Nie mit Geheimnissen in der Meldung. */
class SetupException extends RuntimeException
{
    public function __construct(public readonly string $errorCode, string $message = '', public readonly int $status = 422)
    {
        parent::__construct($message !== '' ? $message : $errorCode);
    }
}
