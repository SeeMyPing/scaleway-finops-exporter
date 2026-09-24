# Contributing

Thanks for your interest in `scaleway-finops-exporter`! Issues and pull
requests are welcome.

## Getting started

You need Go (the version in `go.mod`), `make`, and for the full check suite
`promtool`, `kustomize` and `kubeconform`.

```sh
make tools        # golangci-lint, govulncheck, gofumpt at the pinned versions
make lint test-race
make ci           # everything the CI runs, except the image build and scan
```

No Scaleway account is needed: every test runs against fakes or an
`httptest` server. `make test-integration` runs the opt-in integration tests
against the real API with your `SCW_*` credentials; never paste their output
in an issue without checking it for identifiers.

## Guidelines

- Read [CLAUDE.md](CLAUDE.md) for the project conventions and the
  [ADRs](docs/adr) for the design decisions.
- Use only the official Scaleway SDK to call the API, and check every SDK
  type with `go doc` before using it.
- Keep interfaces small and declared by their consumer. No global state, no
  `init()`, no default Prometheus registry.
- Write table-driven tests; use `testing/synctest` rather than `time.Sleep`.
  Collectors are tested against golden files: after an intended change, run
  `go test ./internal/collector -update` and review the diff.
- Follow the [Prometheus naming conventions](https://prometheus.io/docs/practices/naming/)
  and document every new metric in the README table.
- Record non-obvious decisions in a short ADR in `docs/adr/`.
- Update `CHANGELOG.md` under `[Unreleased]`.

## Commits and pull requests

- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):
  `feat(finops): ...`, `fix(billing): ...`, `docs: ...`, `ci: ...`.
- Keep pull requests focused. The CI must be green: lint, race tests with at
  least 80 % coverage on `internal/`, govulncheck, promtool checks, image
  build and scan.
- `main` is protected by the rulesets in [`.github/rulesets`](.github/rulesets):
  every change goes through a pull request approved by the maintainer
  ([CODEOWNERS](.github/CODEOWNERS)), who is also the only one able to merge.

## Releases

Maintainers tag `vX.Y.Z` on `main`. The release workflow builds the archives
and the multi-arch image, signs them with cosign and attaches SBOMs and SLSA
provenance.
