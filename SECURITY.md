# Security policy

## Supported versions

Only the latest release receives security fixes.

## Reporting a vulnerability

Please **do not open a public issue**. Report vulnerabilities privately
through [GitHub security advisories](https://github.com/SeeMyPing/scaleway-finops-exporter/security/advisories/new).
You should receive an answer within a week. Please include the version, the
configuration (without credentials) and the steps to reproduce.

## Security model

- The exporter only needs **read-only** access: the `BillingReadOnly` and
  `EnvironmentalImpactReadOnly` permission sets. Use a dedicated IAM
  application and API key, and rotate the key regularly.
- Credentials are read from the Scaleway SDK profile, `SCW_*` environment
  variables, or a file (`--scaleway.secret-key-file`), which lets Kubernetes
  mount the secret instead of exposing it in the environment.
- Secrets are never logged, exported as labels, or returned in errors: SDK
  validation errors that could quote the secret key are redacted.
- The exporter serves billing data that may be sensitive. Protect
  `/metrics` with TLS and basic auth (`--web.config.file`, see the
  [exporter-toolkit documentation](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md))
  or a network policy.
- The container image is distroless, runs as a non-root user, and works with
  a read-only root filesystem and no Linux capabilities.
- Releases are signed keylessly with cosign and come with SBOMs and SLSA build
  provenance. Dependencies are scanned by govulncheck, the image by Trivy,
  and the code by CodeQL.
