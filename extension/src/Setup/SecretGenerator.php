<?php

namespace PebBackups\Setup;

/**
 * Zufallswerte fuer das Gateway. Nur Buchstaben und Ziffern, damit sie in .env, Shell und
 * Egg-Variablen ohne Quoting funktionieren. random_int ist kryptografisch sicher.
 */
final class SecretGenerator
{
    public const ALPHANUMERIC = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
    public const UPPER_DIGITS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789';

    public static function random(int $length, string $alphabet = self::ALPHANUMERIC): string
    {
        if ($length < 1 || $alphabet === '') {
            throw new \InvalidArgumentException('Length and alphabet must not be empty.');
        }
        $max = strlen($alphabet) - 1;
        $out = '';
        for ($i = 0; $i < $length; $i++) {
            $out .= $alphabet[random_int(0, $max)];
        }

        return $out;
    }

    public static function accessKey(): string
    {
        return self::random(20, self::UPPER_DIGITS);
    }

    public static function secretKey(): string
    {
        return self::random(40);
    }

    /** Das Gateway verlangt mindestens 32 Zeichen. */
    public static function adminToken(): string
    {
        return self::random(48);
    }

    public static function resticPassword(): string
    {
        return self::random(48);
    }
}
