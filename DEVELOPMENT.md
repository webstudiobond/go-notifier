# go-notifier Development Guide

This guide covers local development, code validation, testing standards, build workflows, and end-to-end integration testing for `go-notifier`.

---

## 1. Prerequisites

Ensure the following tools and dependencies are installed prior to local development and validation:

- **Git**
- **Go 1.27+**
- **GNU Make**
- **Docker & Docker Compose**
- **golangci-lint**
- **govulncheck**
- **trivy**
- **hadolint**
- **curl** (with Unix domain socket support: `--unix-socket`)

---

## 2. Repository Initialization

1. **Clone the repository:**
   ```bash
   git clone https://github.com/webstudiobond/go-notifier
   cd go-notifier
   ```

2. **Verify tooling availability:**
   ```bash
   go version
   golangci-lint --version
   hadolint --version
   trivy --version
   govulncheck --version
   ```

---

## 3. Code Validation & Testing

The project enforces strict code quality and security standards across all Go source files, container manifests, and dependencies. All quality gates are centralized in the `Makefile`.

1. **Go Formatting, Linting & Compilation Verification:**
   Validates formatting via `gofmt -s`, executes static analysis configured in `.golangci.yml`, and verifies compilation:
   ```bash
   make check
   ```

2. **Go Unit Tests (with Race Detector):**
   Executes the entire automated unit test suite with the Go race detector enabled (`-race`):
   ```bash
   make test
   ```

3. **Go Code Coverage:**
   Executes unit tests and displays per-function coverage metrics:
   ```bash
   make coverage
   ```

4. **Go Vulnerability Scanning:**
   Scans Go source code and dependencies for known CVEs using `govulncheck`:
   ```bash
   make vulncheck
   ```

5. **Filesystem Security Scanning:**
   Audits the repository filesystem for vulnerabilities using `trivy`:
   ```bash
   make trivy
   ```

6. **Containerfile Linting:**
   Lints `Dockerfile` against security and best-practice rules via `hadolint`:
   ```bash
   make docker-lint
   ```

7. **Comprehensive Project Verification:**
   Executes the complete verification suite (`check`, `test`, `vulncheck`, `trivy`, `docker-lint`). **Always run this command prior to opening a pull request or cutting a release:**
   ```bash
   make verify
   ```

8. **Clean Build & Test Artifacts:**
   Removes generated coverage profiles and caches:
   ```bash
   make clean
   ```

---

## 4. Build & Container Workflows

1. **Compile Static Binary Locally:**
   Compiles a statically linked, stripped binary with embedded version metadata:
   ```bash
   CGO_ENABLED=0 go build \
     -ldflags="-s -w -X 'main.Version=dev'" \
     -trimpath \
     -o bin/go-notifier \
     ./cmd/go-notifier
   ```

2. **Cross-Compile for Target Architectures:**
   Cross-compile for specific target architectures by passing `GOOS` and `GOARCH`:
   ```bash
   # Linux AMD64 (x86_64):
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
     -ldflags="-s -w -X 'main.Version=1.0.0'" \
     -trimpath \
     -o bin/go-notifier-linux-amd64 \
     ./cmd/go-notifier

   # Linux ARM64 (aarch64):
   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
     -ldflags="-s -w -X 'main.Version=1.0.0'" \
     -trimpath \
     -o bin/go-notifier-linux-arm64 \
     ./cmd/go-notifier
   ```

3. **Build Scratch Container Image:**
   Builds the minimal, unprivileged container image based on `scratch`:
   ```bash
   docker build -t go-notifier:dev .
   ```

---

## 5. Local Integration & End-to-End Testing

This section details running and verifying the daemon locally via its Unix domain socket interface.

### 5.1 Environment Setup

Prepare local directory structures for sockets and secrets:

```bash
mkdir -p dev/sockets dev/secrets
chmod 0700 dev/secrets
```

### 5.2 Configure Development Secrets

Populate test secrets in `dev/secrets/`:

```bash
# Mandatory SMTP secrets (if testing SMTP)
echo "mail.example.com" > dev/secrets/smtp_host.txt
echo "465" > dev/secrets/smtp_port.txt
echo "alerts@example.com" > dev/secrets/smtp_mail.txt
echo "testpassword" > dev/secrets/smtp_password.txt

# Admin recipient whitelist
echo "admin@example.com" > dev/secrets/admin_emails.txt

# Restrict permissions
chmod 0400 dev/secrets/*.txt
```

### 5.3 Run Daemon Locally

Execute the compiled binary with local paths:

```bash
MANDATORY_SECRETS_DIR=./dev/secrets \
OPTIONAL_SECRETS_DIR=./dev/secrets \
NOTIFY_SOCKET_PATH=./dev/sockets/notify.sock \
./bin/go-notifier
```

### 5.4 Test API Endpoints via Socket

1. **Verify Liveness Probe (`GET /healthz`):**
   ```bash
   # Via curl:
   curl --unix-socket ./dev/sockets/notify.sock http://_/healthz

   # Or via built-in CLI healthcheck probe (used by Docker healthcheck):
   ./bin/go-notifier healthcheck ./dev/sockets/notify.sock
   ```

2. **Dispatch Test Notification (`POST /notify`):**
   ```bash
   curl --unix-socket ./dev/sockets/notify.sock http://_/notify \
     -H "Content-Type: application/json" \
     -d '{
       "subject": "Local Integration Test Alert",
       "to": ["admin@example.com"],
       "body_text": "Testing socket IPC communication."
     }'
   ```

3. **Test Push-Only Mode (No SMTP):**
   ```bash
   NOTIFY_SMTP_ENABLED=false \
   OPTIONAL_SECRETS_DIR=./dev/secrets \
   NOTIFY_SOCKET_PATH=./dev/sockets/notify.sock \
   ./bin/go-notifier
   ```

---

## 6. Makefile Targets Reference

| Target | Description |
| :--- | :--- |
| `make check` | Validates formatting with `gofmt -s`, runs `golangci-lint`, and verifies compilation. |
| `make test` | Executes unit tests with the race detector enabled (`-race`). |
| `make coverage` | Generates a coverage profile (`coverage.out`) and displays per-function statistics. |
| `make vulncheck` | Analyzes source code and dependencies for known CVEs using `govulncheck`. |
| `make trivy` | Scans repository filesystem for `CRITICAL` and `HIGH` vulnerabilities with `trivy`. |
| `make docker-lint` | Lints `Dockerfile` against best practices and security rules using `hadolint`. |
| `make verify` | Complete quality gate executing `check`, `test`, `vulncheck`, `trivy`, and `docker-lint`. |
| `make clean` | Removes generated test coverage profiles, cache directories, and build artifacts. |
