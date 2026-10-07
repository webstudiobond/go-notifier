# go-notifier

[![CI](https://github.com/webstudiobond/go-notifier/actions/workflows/ci.yml/badge.svg)](https://github.com/webstudiobond/go-notifier/actions/workflows/ci.yml)
[![GitHub last commit](https://img.shields.io/github/last-commit/webstudiobond/go-notifier)](https://github.com/webstudiobond/go-notifier/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/webstudiobond/go-notifier)](https://github.com/webstudiobond/go-notifier/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/webstudiobond/go-notifier)](https://github.com/webstudiobond/go-notifier)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A lightweight, decoupled notification and email dispatch daemon. While originally engineered as an out-of-process companion for the hardened [`wordpress-docker`](https://github.com/webstudiobond/wordpress-docker) production stack, `go-notifier` is completely application-agnostic and serves as a secure notification sidecar for any web application or microservice (PHP, Python, Node.js, Go, Rust, Ruby).

The service runs as a zero-dependency, non-root `scratch` container, isolating mail transport credentials and multi-channel alert dispatch logic behind a local Unix domain socket.

---

## Motivation

Hardened production environments (such as the [`wordpress-docker`](https://github.com/webstudiobond/wordpress-docker) runtime) enforce strict zero-trust containment: immutable root filesystems (`read_only: true`), dropped Linux capabilities (`cap_drop: [ALL]`), shell-less container runtimes (complete removal of shell binaries), and the total elimination of local Mail Transfer Agents (Sendmail, Exim, Postfix).

Traditional web application email architectures introduce severe security liabilities:
- **In-Container MTAs**: Rely on setuid binaries and shell execution (`/usr/sbin/sendmail`). In the event of an application-layer Remote Code Execution (RCE) flaw, attackers routinely weaponize local MTA binaries to break containment or spawn interactive shells.
- **Direct Outbound SMTP from App Workers**: Storing SMTP credentials in application memory or environment exposes them to memory disclosure vulnerabilities, while opening outbound network sockets directly from worker threads expands container attack surface and blocks execution during external TLS handshakes.

---

<details>
<summary><strong>Architecture</strong></summary>

## Architecture Highlights

`go-notifier` decouples notification and mail transport into an isolated, unprivileged scratch sidecar:

```
[ Application (PHP / Python / Node / Go) ]
        │
        │ HTTP / JSON over Unix Domain Socket
        ▼ (/var/run/sockets/notify/notify.sock - mode 0600)
[ go-notifier (scratch daemon) ]
   ├── Rate Limiting (Token Bucket)
   ├── Recipient Filter (admin_emails check)
   └── Routing Engine (Subject Regex)
        ├── SMTP Relay (Port 465 / 587 TLS)
        ├── Telegram Bot API (HTTPS)
        ├── Matrix Homeserver API (HTTPS)
        └── ntfy Endpoint (HTTPS)
```

* **Shell-less Scratch Container:** Statically compiled Go binary (`CGO_ENABLED=0`) running in an empty `scratch` image with trusted CA certificates. No shell binaries, no package manager, zero local attack surface.
* **Pure UNIX Domain Socket IPC:** Zero exposed TCP network ports to Docker bridge or host. Communication occurs strictly over an in-memory Unix domain socket (`notify.sock`) shared via tmpfs with `0600` permissions restricted to matching UID/GID (`APP_UID:APP_GID`).
* **Zero-Privilege Security Profile:** Immutable root filesystem (`read_only: true`), all Linux capabilities dropped (`cap_drop: [ALL]`), no capabilities added (`cap_add: []`), privilege escalation blocked (`no-new-privileges: true`), and strict resource limits (64 MB RAM, 0.5 CPU, 30 PIDs).
* **Strict Docker Secrets:** SMTP relay credentials and messenger API tokens are mounted exclusively from disk secret files (`0400`). Zero sensitive credentials exist in container environment variables or command-line arguments.
* **Granular Multi-Channel Dispatch:** Intelligently routes notifications across SMTP, Telegram, Matrix, and ntfy using subject regular expression matching, administrator recipient filtering (`admin_emails.txt`), and per-rule attachment forwarding policies.
* **Startup Validation:** Validates channel credentials and routing rules on boot. If an active rule or default target references an unconfigured channel or disabled SMTP, the daemon terminates immediately with a descriptive error.

### Universal Applicability Beyond WordPress

Although designed for the [`wordpress-docker`](https://github.com/webstudiobond/wordpress-docker) runtime, `go-notifier` contains **no WordPress-specific dependencies, protocols, or logic**.

Any software stack capable of dispatching an HTTP POST request over a local Unix socket can leverage `go-notifier`:
- **Modern Frameworks & Backends**: Laravel, Symfony, FastAPI, Django, Flask, Express, NestJS, Ruby on Rails, Go, or Rust services.
- **Zero-MTA / Distroless Environments**: Ideal for distroless or minimal Alpine/Debian images where installing full MTA suites is prohibited by security policy.
- **Microservices & Kubernetes Sidecars**: Functions as a shared sidecar container within a Kubernetes Pod or Docker Compose project, abstracting TLS handshakes, rate limiting, and push messenger routing away from core application code.

</details>

---

<details>
<summary><strong>Supported Channels & Protocols</strong></summary>

## Supported Channels & Protocols

| Channel | Protocol | Transport Security | Payload Support |
| :--- | :--- | :--- | :--- |
| **SMTP** | SMTP / ESMTP | Implicit TLS (Port 465) or STARTTLS (Port 587) | Text, HTML, MIME Attachments |
| **Telegram** | HTTP POST | TLS 1.3 (`api.telegram.org`) | Markdown Text (auto-truncated at 3500 chars), Document Attachments |
| **Matrix** | Client-Server API v3 | TLS 1.3 (Direct Homeserver endpoint) | Plain / HTML Text, `m.file` Events |
| **ntfy** | HTTP POST | TLS 1.3 (Self-hosted or `ntfy.sh`) | Plain Text, Binary Attachments |

</details>

---

<details>
<summary><strong>API Specification</strong></summary>

## API Specification

The daemon listens on the configured Unix domain socket (`/var/run/sockets/notify/notify.sock`).

### `POST /notify` (or `POST /`)

Dispatches an outbound notification or email message.

#### Request Headers
```http
Content-Type: application/json
```

#### Request Body
```json
{
  "subject": "Critical Security Alert: Brute-force detected",
  "body_text": "IP 192.0.2.1 blocked after 5 failed attempts.",
  "body_html": "<p>IP <code>192.0.2.1</code> blocked after 5 failed attempts.</p>",
  "reply_to": "security@example.com",
  "to": [
    "admin@example.com"
  ],
  "attachments": [
    {
      "filename": "audit-report.pdf",
      "mime_type": "application/pdf",
      "content_base64": "JVBERi0xLjQKJ..."
    }
  ]
}
```

#### Field Definitions
- `subject` *(string, optional)*: Message subject used for mail headers and regex route evaluation. At least one of `subject`, `body_text`, or `body_html` is required.
- `body_text` *(string, optional)*: Plain-text fallback content.
- `body_html` *(string, optional)*: HTML-formatted content.
- `reply_to` *(string, optional)*: Email address for `Reply-To` header.
- `to` *(array of strings, optional)*: Destination email addresses. Mandatory for SMTP delivery; optional for messenger-only notifications.
- `attachments` *(array of objects, optional)*: File attachments encoded in Base64.

#### Response Codes
- `200 OK`: `{"status":"delivered"}` — Payload successfully dispatched to all active channels.
- `400 Bad Request`: `{"error":"..."}` — Malformed JSON, completely empty notification (neither subject nor body provided), or missing recipients (`to`) when routed exclusively to SMTP.
- `413 Payload Too Large`: `{"error":"..."}` — Attachment size exceeds `NOTIFY_MAX_ATTACHMENT_SIZE_MB`.
- `429 Too Many Requests`: `{"error":"rate limit exceeded"}` — Dispatch frequency throttled by token bucket.
- `502 Bad Gateway`: `{"error":"dispatch failed: ..."}` — Relay failure reported by upstream transport.

#### Example: Client Dispatch via Unix Socket
```bash
curl --unix-socket /var/run/sockets/notify/notify.sock http://_/notify \
  -H "Content-Type: application/json" \
  -d '{
    "subject": "System Security Alert",
    "to": ["admin@example.com"],
    "body_text": "Failed authentication threshold exceeded."
  }'
```

> **Note on URL Syntax**: The host `_` in `http://_/notify` is a dummy placeholder required by `curl` to construct a valid HTTP/1.1 request line and `Host:` header. Because `--unix-socket` routes all traffic directly to the local socket file, no DNS lookup or TCP networking occurs. Do not replace `_` with a real hostname or IP address.

### `GET /healthz`

Liveness probe endpoint.

```bash
curl --unix-socket /var/run/sockets/notify/notify.sock http://_/healthz
```

#### Response
```json
{"status":"ok"}
```

</details>

---

<details>
<summary><strong>Configuration Reference</strong></summary>

## Configuration Reference

Configuration is supplied via environment variables (operational knobs) and secret files (credentials).

### Operational Environment Variables

| Variable | Default | Description |
| :--- | :--- | :--- |
| `SITE_NAME` | `WordPress Site` | Site identifier prefixed to notifications (e.g. `[SiteName] Subject`). Fallback: `SITE_USER` -> `SERVER_NAME`. |
| `SMTP_FROM_NAME` | None | Optional sender display name formatted into outbound email `From` header (e.g. `"Display Name" <noreply@example.com>`). |
| `NOTIFY_SMTP_ENABLED` | `true` | When `true`, SMTP relay is enabled and mandatory (requires SMTP secrets). When `false`, SMTP is completely disabled and secrets are ignored. |
| `NOTIFY_ADMIN_FILTER_REQUIRED` | `true` | Evaluated only when `NOTIFY_SMTP_ENABLED=true`. When `true`, messenger delivery requires recipient matching in `admin_emails.txt`. When `false`, recipient filtering is bypassed. Ignored when `NOTIFY_SMTP_ENABLED=false`. |
| `NOTIFY_CHANNELS` | Auto-detected | Comma-separated default channels for admin alerts (`smtp,telegram,matrix,ntfy`). If unset, automatically aggregates all enabled channels whose credentials are present. Explicitly requesting an unconfigured channel causes a fail-fast validation error at startup. |
| `NOTIFY_RATE_LIMIT_PER_MINUTE` | `10` | Token bucket refill rate. Set to `0` to disable throttling. |
| `NOTIFY_BURST` | `5` | Maximum token bucket burst capacity. |
| `NOTIFY_MAX_ATTACHMENT_SIZE_MB` | `10` | Maximum size per individual attachment in megabytes. Set to `0` to disable limit. |

### Operational Modes Matrix

The interaction between `NOTIFY_SMTP_ENABLED` and `NOTIFY_ADMIN_FILTER_REQUIRED` defines three distinct operational modes:

| Mode | `NOTIFY_SMTP_ENABLED` | `NOTIFY_ADMIN_FILTER_REQUIRED` | Operational Behavior |
| :--- | :--- | :--- | :--- |
| **Strict Isolation (Default)** | `true` | `true` | SMTP is mandatory. Non-admin recipients route strictly to SMTP. Messenger alerts require recipient match in `admin_emails.txt`; if empty, messengers receive nothing. |
| **Open Multi-Channel** | `true` | `false` | SMTP is mandatory. Recipient filtering is disabled. All messages route to matched rule targets or `NOTIFY_CHANNELS` (both SMTP and messengers). |
| **Push-Only Sidecar (No SMTP)** | `false` | `*` (Ignored) | SMTP is completely disabled; all SMTP secrets and channels are ignored. Messages dispatch strictly to messengers (Telegram, Matrix, ntfy). Recipient filtering is bypassed. |

</details>

---

<details>
<summary><strong>Granular Routing Engine</strong></summary>

## Granular Routing Engine

Administrative alerts can be selectively dispatched to distinct channels and granted attachment permissions based on subject regular expressions:

```ini
NOTIFY_RULE_<NAME>_MATCH="<regex>"
NOTIFY_RULE_<NAME>_TARGETS="<channel1,channel2>"
NOTIFY_RULE_<NAME>_ATTACHMENTS="true|false"
```

### Rule Evaluation Logic

1. **Recipient Filtering**: When both `NOTIFY_SMTP_ENABLED=true` and `NOTIFY_ADMIN_FILTER_REQUIRED=true`, messages whose recipients do not match `admin_emails.txt` are relayed exclusively to `smtp`. When filtering is disabled (`false`) or SMTP is disabled, this check is bypassed.
2. **Subject Matching**: For eligible messages, rules are scanned against `subject`.
   - On match: Message is dispatched to channels specified in `NOTIFY_RULE_<NAME>_TARGETS` (or `NOTIFY_CHANNELS` if targets are omitted). Attachments are forwarded only if `NOTIFY_RULE_<NAME>_ATTACHMENTS=true`.
3. **Default Fallback**: If an eligible message matches no custom rules, it is routed to `NOTIFY_CHANNELS` with attachments disabled (`false`).
4. **Multi-Target Resilience**: If a message has no `to` recipients and targets both `smtp` and messengers, `smtp` delivery is skipped without failing the request, and the alert is successfully delivered to all active push messengers. Only when a message is routed *exclusively* to `smtp` without recipients will it return `400 Bad Request`.

### Startup Validation

The daemon validates all configured channels and routing targets during initialization:
- If a channel specified in `NOTIFY_CHANNELS` or any routing rule (`NOTIFY_RULE_<NAME>_TARGETS`) lacks required credentials, or if `smtp` is requested while `NOTIFY_SMTP_ENABLED=false`, the daemon terminates startup with a validation error.
- If SMTP is disabled (`NOTIFY_SMTP_ENABLED=false`) and no messenger secrets are configured, the daemon terminates startup immediately (`no notification channels configured`).

### Configuration Examples

```ini
# Route security and intrusion alerts (Wordfence) to Matrix and SMTP without attachments
NOTIFY_RULE_WORDFENCE_MATCH="(?i)wordfence|security alert|attack"
NOTIFY_RULE_WORDFENCE_TARGETS="smtp,matrix"
NOTIFY_RULE_WORDFENCE_ATTACHMENTS=false

# Route job applications and contact forms to ntfy and SMTP with attachments
NOTIFY_RULE_FORMS_MATCH="(?i)contact form|job application|resume"
NOTIFY_RULE_FORMS_TARGETS="smtp,ntfy"
NOTIFY_RULE_FORMS_ATTACHMENTS=true

# Route store orders to Telegram and SMTP without attachments
NOTIFY_RULE_ORDERS_MATCH="(?i)new order|order #[0-9]+"
NOTIFY_RULE_ORDERS_TARGETS="smtp,telegram"
NOTIFY_RULE_ORDERS_ATTACHMENTS=false
```

</details>

---

<details>
<summary><strong>Deployment & Setup</strong></summary>

## Deployment & Setup (Docker Compose)

### Directory Structure

```text
├── docker-compose.yaml              # Production deployment manifest
├── .env                             # Host infrastructure variables (UID, GID, site user)
├── notifier.env                     # Optional: channels, routing rules, and limits
├── secrets/                         # Docker secrets directory (owner-only access, mode 0700)
│   ├── smtp_host.txt                # Mandatory if NOTIFY_SMTP_ENABLED=true: relay hostname or IP
│   ├── smtp_port.txt                # Mandatory if NOTIFY_SMTP_ENABLED=true: port (465 or 587)
│   ├── smtp_mail.txt                # Mandatory if NOTIFY_SMTP_ENABLED=true: sender email / username
│   ├── smtp_password.txt            # Mandatory if NOTIFY_SMTP_ENABLED=true: authentication password
│   ├── admin_emails.txt             # Required for messengers when SMTP & admin filter are enabled (default)
│   ├── telegram_bot_token.txt       # Optional: Telegram Bot API token
│   ├── telegram_chat_id.txt         # Optional: Telegram target chat ID
│   ├── matrix_url.txt               # Optional: Matrix homeserver URL
│   ├── matrix_room_id.txt           # Optional: Matrix internal room ID
│   ├── matrix_access_token.txt      # Optional: Matrix bot access token
│   ├── ntfy_url.txt                 # Optional: ntfy server URL
│   ├── ntfy_topic.txt               # Optional: ntfy topic name
│   └── ntfy_token.txt               # Optional: ntfy Bearer authentication token
```

### Step-by-Step Deployment Guide

#### Prerequisites

* **Docker Engine & Compose:** Ensure Docker Engine and Docker Compose plugin are installed on the host. Follow the official installation guide for [Ubuntu](https://docs.docker.com/engine/install/ubuntu/#install-using-the-repository).

#### 1. Identify or Create Dedicated System User

For multi-tenant isolation and security compliance, run `go-notifier` under a dedicated unprivileged user (matching your web application worker):

```bash
SITE_USER=mysite
sudo useradd -m -d /home/${SITE_USER} -s /usr/sbin/nologin ${SITE_USER}
```

Identify the UID and GID to bind container execution and socket file ownership:

```bash
APP_UID=$(id -u ${SITE_USER})
APP_GID=$(id -g ${SITE_USER})
```

*(If deploying within an existing application directory without creating a new user, inspect your current user: `SITE_USER=$(whoami); APP_UID=$(id -u); APP_GID=$(id -g)`)*

#### 2. Create Directory Structure

Create the secrets directory with owner-only access:

```bash
sudo -u ${SITE_USER} mkdir -p /home/${SITE_USER}/secrets
sudo chmod 0700 /home/${SITE_USER}/secrets
```

#### 3. Generate Secrets

Create the secret files for your configured channels:

**SMTP:**
```bash
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/smtp_host.txt
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/smtp_port.txt
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/smtp_mail.txt
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/smtp_password.txt
```

**Telegram:**
```bash
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/telegram_bot_token.txt
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/telegram_chat_id.txt
```

**Matrix:**
```bash
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/matrix_url.txt
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/matrix_room_id.txt
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/matrix_access_token.txt
```

**ntfy:**
```bash
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/ntfy_url.txt
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/ntfy_topic.txt
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/ntfy_token.txt
```

**Admin recipient whitelist (`admin_emails.txt`):**
```bash
sudo -u ${SITE_USER} nano /home/${SITE_USER}/secrets/admin_emails.txt
```

Lock secret files with strict read-only permissions:

```bash
sudo chmod 0400 /home/${SITE_USER}/secrets/*.txt
```

#### 4. Download Compose Manifest and Environment Files

Download configuration templates directly from the repository using `curl`:

```bash
REPO="https://raw.githubusercontent.com/webstudiobond/go-notifier/main"

sudo -u ${SITE_USER} curl -fsSL ${REPO}/docker-compose.yaml -o /home/${SITE_USER}/docker-compose.yaml
sudo -u ${SITE_USER} curl -fsSL ${REPO}/example.env -o /home/${SITE_USER}/.env
sudo -u ${SITE_USER} curl -fsSL ${REPO}/notifier.env.example -o /home/${SITE_USER}/notifier.env
```

#### 5. Configure Environment

Edit `.env` to supply `SITE_USER`, `APP_UID`, and `APP_GID` identified in Step 1:

```bash
sudo -u ${SITE_USER} nano /home/${SITE_USER}/.env
```

See [example.env](example.env) for all available variables.

#### 6. (Optional) Configure Routing & Channels

Customize active channels, rate limits, and regex routing rules in `notifier.env`:

```bash
sudo -u ${SITE_USER} nano /home/${SITE_USER}/notifier.env
```

See [notifier.env.example](notifier.env.example) for all available options, channels, and regex routing rules.

#### 7. Set Permissions

Enforce strict ownership and access rights across the deployment directory:

```bash
sudo chown -R ${SITE_USER}:${SITE_USER} /home/${SITE_USER}
sudo chmod 0700 /home/${SITE_USER}/secrets
sudo chmod 0400 /home/${SITE_USER}/secrets/*.txt
sudo chmod 0600 /home/${SITE_USER}/.env
[ -f /home/${SITE_USER}/notifier.env ] && sudo chmod 0600 /home/${SITE_USER}/notifier.env
```

#### 8. Start the Stack

Pull the scratch image and start the service:

```bash
docker compose -f /home/${SITE_USER}/docker-compose.yaml pull
docker compose -f /home/${SITE_USER}/docker-compose.yaml up -d
```

Follow daemon logs to confirm channel initialization:

```bash
docker compose -f /home/${SITE_USER}/docker-compose.yaml logs -f notifier
```

Verify service liveness via the Unix domain socket:

```bash
curl --unix-socket /var/run/sockets/notify/notify.sock http://_/healthz
```

To stop the service:

```bash
docker compose -f /home/${SITE_USER}/docker-compose.yaml down
```

### Example: Standalone Push Sidecar (No SMTP, Pure Messengers)

For microservices and environments requiring only push alerts (Telegram, Matrix, ntfy) without SMTP relay infrastructure:
1. In `notifier.env`, set `NOTIFY_SMTP_ENABLED=false`.
2. In `docker-compose.yaml`, comment out the 4 `smtp_*` secret entries under `secrets:`.
3. Provide required messenger secrets in `secrets/`.

</details>

---

<details>
<summary><strong>Development</strong></summary>

## Development & Testing

For instructions on building from source, running the test suite, and local verification gates, refer to [DEVELOPMENT](DEVELOPMENT.md).

</details>
