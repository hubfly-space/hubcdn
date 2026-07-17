# Contributing to hubCDN

Thanks for your interest in contributing! This document covers the workflow,
conventions, and checks you need to follow.

## Getting started

### Prerequisites

- **Go** (version specified in `go.mod`)
- **[templ](https://templ.guide/)** — `go install github.com/a-h/templ/cmd/templ@v0.3.1020`
- **Docker** (optional, for building/running the container image)

### Local development

```sh
# Clone the repo
git clone https://github.com/hubfly-space/hubcdn.git
cd hubcdn

# Start the dev server (staging ACME, local data directory)
make dev
```

The `dev` target runs `templ generate` then `go run` with staging ACME enabled.
It listens on `https://localhost:4403`.

### Building

```sh
make build    # templ generate + go build
make docker-build  # production Docker image
```

## Making changes

### Code style

- All Go code must pass **`gofmt`**. Run `make fmt` before committing.
- Run `make check` to verify formatting, vetting, and tests all pass.
- Follow the existing code conventions — there are no linter configs, so match
  the style of the file you're editing.

### Templ templates

After editing any `.templ` file under `internal/web/views/`, regenerate the
Go code:

```sh
make generate
```

The generated `*_templ.go` files **must be committed**. CI verifies they are
up to date. If you forget, the `templ verify` step will fail.

### Running tests

```sh
make test        # go test ./...
make test-race   # go test -race ./...
```

Tests use the standard `testing` package — no external test framework is
required.

### Before submitting a PR

Run the full check suite locally:

```sh
make check
```

This runs, in order:
1. `gofmt` — formatting check
2. `go vet` — static analysis
3. `go test` — unit tests

All three must pass. CI will also run `templ verify` and `go test -race`.

## Submitting a pull request

1. **Fork** the repository and create a branch from `main`.
2. Make your changes, keeping commits focused and well-described.
3. Run `make check` to ensure everything passes.
4. Open a pull request against `main`.

### PR guidelines

- Keep PRs small and focused on a single change when possible.
- Include context in the PR description — what the change does and why.
- If the PR addresses an existing issue, reference it (e.g., "Closes #13").
- Add screenshots or recordings for UI changes.
- New features should include tests where practical.

## Project structure

```
cmd/hubcdn/          Entry point (main.go, healthcheck.go)
internal/
  bunny/             Bunny DNS API client (multi-node registration)
  cache/             In-memory cache, memory watchdog, HTTP caching policy
  certguard/         ACME rate-limit guard
  config/            Environment variable configuration loader
  dnsx/              DNS resolver for TXT/A/AAAA lookups
  domain/            Domain lifecycle, settings, registry
  imageproc/         Image transformation pipeline
  imgcdn/            /img/ HTTP handler
  proxy/             Caching reverse proxy, WebSocket tunnel
  server/            HTTPS listener, routing, component wiring
  web/               HTML page rendering
    views/           templ templates and generated Go code
docs/                Project documentation
```

## Reporting bugs

Open an issue with:
- A clear title and description
- Steps to reproduce (if applicable)
- Expected vs actual behavior
- Environment details (OS, Go version, Docker version)

## Code of conduct

Be respectful and constructive. We're all here to build something useful.
