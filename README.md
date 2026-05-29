Tinfoil bucket. Made to run in an enclave. Multi-tenant.

Uses [tinfoil-bucket-sidecar](https://github.com/tinfoilsh/tinfoil-buckets-sidecar) in multi-tenant mode

Architecture:
web-app -(unencrypted data & auth. encKey?)--> tinfoil-bucket -(encrypted data)--> S3

Inside bucket:
~S3 API -> check for Auth -> multi-tenant buckets sidecar
