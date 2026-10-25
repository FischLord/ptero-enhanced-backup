<?php

namespace PebBackups\Setup;

use Throwable;

/** S3-Anfragen ueber den S3-Client des Panels (BackupManager), also mit genau den Zugangsdaten, die Wings-Backups nutzen. */
final class PanelS3Prober implements S3Prober
{
    public const MANAGER = 'Pterodactyl\\Extensions\\Backups\\BackupManager';
    public const DISK = 'Pterodactyl\\Extensions\\Filesystem\\S3Filesystem';

    public static function supported(): bool
    {
        return class_exists(self::MANAGER) && class_exists(self::DISK)
            && method_exists(self::MANAGER, 'adapter') && method_exists(self::DISK, 'getClient') && method_exists(self::DISK, 'getBucket');
    }

    public function create(string $key): string
    {
        [$client, $bucket] = $this->client();

        try {
            $result = $client->execute($client->getCommand('CreateMultipartUpload', $this->params($bucket, $key)));
        } catch (Throwable $e) {
            throw $this->map($e);
        }

        $id = $result['UploadId'] ?? null;
        if (! is_string($id) || $id === '') {
            throw new SetupException('s3_error', 'The S3 endpoint did not return an upload ID.', 502);
        }

        return $id;
    }

    public function abort(string $key, string $uploadId): void
    {
        [$client, $bucket] = $this->client();

        try {
            $client->execute($client->getCommand('AbortMultipartUpload', $this->params($bucket, $key) + ['UploadId' => $uploadId]));
        } catch (Throwable $e) {
            throw $this->map($e);
        }
    }

    /** @return array<string, mixed> */
    private function params(string $bucket, string $key): array
    {
        // Kurze Fristen und keine Wiederholungen: Ein unerreichbares Gateway soll schnell als Fehler erscheinen.
        return ['Bucket' => $bucket, 'Key' => $key, '@retries' => 0, '@http' => ['connect_timeout' => 5, 'timeout' => 10]];
    }

    /** @return array{0: \Aws\S3\S3ClientInterface, 1: string} */
    private function client(): array
    {
        if (! self::supported()) {
            throw new SetupException('no_s3_disk', 'The panel has no S3 backup disk.', 501);
        }

        try {
            $disk = app(self::MANAGER)->adapter();
        } catch (Throwable) {
            throw new SetupException('no_s3_disk', 'The panel has no S3 backup disk.');
        }
        $class = self::DISK;
        if (! $disk instanceof $class) {
            throw new SetupException('no_s3_disk', 'The panel backup driver is not S3.');
        }

        return [$disk->getClient(), $disk->getBucket()];
    }

    private function map(Throwable $e): SetupException
    {
        $status = method_exists($e, 'getStatusCode') ? $e->getStatusCode() : null;
        $awsCode = method_exists($e, 'getAwsErrorCode') ? $e->getAwsErrorCode() : null;
        $message = $e->getMessage();

        if ($status === 403 || $status === 401 || in_array($awsCode, ['SignatureDoesNotMatch', 'InvalidAccessKeyId', 'AccessDenied'], true)) {
            return new SetupException('s3_denied', 'The S3 endpoint rejected the signed request.', 502);
        }
        if ($status === null || $status === 0) {
            return str_contains($message, 'cURL error 28') || stripos($message, 'timed out') !== false
                ? new SetupException('s3_timeout', 'The S3 endpoint did not answer in time.', 504)
                : new SetupException('s3_unreachable', 'The S3 endpoint is not reachable from the panel server.', 502);
        }

        return new SetupException('s3_error', 'The S3 endpoint reported an error.', 502);
    }
}
