<?php

namespace PebBackups\Http\Controllers;

use Illuminate\Http\JsonResponse;
use PebBackups\Pro\ProRegistry;

class EditionController
{
    /** Ist die Pro-Extension aktiv? Braucht kein Gateway, damit die Hinweise auch bei Ausfall stimmen. */
    public function __invoke(ProRegistry $pro): JsonResponse
    {
        return (new JsonResponse(['pro' => $pro->info()]))->header('Cache-Control', 'no-store');
    }
}
