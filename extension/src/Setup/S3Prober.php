<?php

namespace PebBackups\Setup;

/** Signierte S3-Anfragen mit den Zugangsdaten des Panels (nur die Teilmenge, die das Gateway kann). */
interface S3Prober
{
    /**
     * CreateMultipartUpload auf einem Testschluessel.
     *
     * @return string UploadId
     *
     * @throws SetupException mit Code no_s3_disk, s3_denied, s3_unreachable, s3_timeout oder s3_error
     */
    public function create(string $key): string;

    /** @throws SetupException */
    public function abort(string $key, string $uploadId): void;
}
