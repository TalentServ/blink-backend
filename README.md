# blink-backend

The Go API for the `blink_demo` wizard. The Dockerfile builds `cmd/server`, which is the only supported Blink backend runtime.

> `src/main/java` and `pom.xml` are frozen legacy code. Do not run, deploy, or add features to the Java implementation. They are retained temporarily for reference only and are not a parity target.

Blink does not create repositories, commits, branches, pull requests, Jira issues, Jira comments, Jira transitions, merge changes, or deployments. It provides workflow guidance and manual handoffs; external provider actions remain human-owned.

## Run locally

Go and Postgres are required. Copy `.env.example` when present, configure `DATABASE_URL`, and provide `BLINK_AGENT_RUNTIME_TOKEN` for the agent runtime. `.env` is gitignored.

```powershell
go run ./cmd/server
```

The API listens on `http://localhost:8090`. Vite proxies `/api` to that local port.

In another terminal, from the **frontend** repo (`Development/Blink-Frontend/blink_demo`):

```powershell
npm install
npm run dev
```

Open `http://127.0.0.1:5173`. `.env.development` sets `VITE_API_URL=/api` so the wizard talks to the local Go API.

### Local Python Agent Runtime (optional)

The backend defaults to the live AWS Lambda Agent Runtime (`https://z5i3yybrx1.execute-api.us-west-2.amazonaws.com`). To run the agent from code on this machine instead:

```powershell
cd ..\..\Blink-Framework\automation_sdlc\services\agent-runtime-python
# install requirements
pip install -r requirements.txt
uvicorn app.main:app --port 8787
```

Then in `blink-backend/.env`:

```
BLINK_AGENT_RUNTIME_URL=http://127.0.0.1:8787
BLINK_AGENT_RUNTIME_TOKEN=your-local-agent-token
```

Restart the Go API after changing `.env`.

## Deploy on Render

You already have Postgres on Render. Deploy the API from the **blink-backend** repo (this folder contains the `Dockerfile`). Deploy the UI as a separate static site from `blink_demo`.

### 1. Backend — Web Service (Docker)

The GitHub repo `blink-backend` already *is* the API. The `Dockerfile` sits at the **repo root** (`Dockerfile`, not `blink-backend/Dockerfile`).

1. **New → Web Service** → `nisha-ts-40599/blink-backend`.
2. Runtime: **Docker**.
3. **Root Directory: leave blank.** Do not set `blink-backend` — that folder does not exist in this repo, and Render will fail with "Root directory 'blink-backend' does not exist".
4. Dockerfile path: `Dockerfile` (default).
5. Instance: at least **1 GB RAM** (zip generation is heavy for 512 MB).
6. Health check: `/actuator/health`
7. Environment:

| Key | Value |
| --- | --- |
| `DATABASE_URL` | **Required.** Link the existing Postgres service or paste its internal URL. The Go API applies its own migrations at startup. |
| `BLINK_AGENT_RUNTIME_URL` | `https://z5i3yybrx1.execute-api.us-west-2.amazonaws.com` (AWS Lambda endpoint; defaults to this if omitted) |
| `BLINK_AGENT_RUNTIME_TOKEN` | **Required in production.** Set a rotated runtime token; never use the legacy development value. |
| `BLINK_AUTOMATION_SDLC_GIT_URL` | `https://github.com/AtulTalentServ/automation_sdlc.git` (Docker image includes git so the kit can be cloned when `/app/automation_sdlc` is empty) |
| `BLINK_CORS_ORIGINS` | `https://YOUR-FRONTEND.onrender.com` (add after the static site exists; you can also keep `http://localhost:5173`) |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | **Required for S3 workspaces.** Save & Continue copies the kit to `<slug>_<id>_workspace/`. |
| `AWS_REGION` | `us-west-2` |
| `S3_BUCKET_NAME` | `blink-ai-dev` |
| `S3_PUBLIC_BASE_URL` | `https://blink-ai-dev.s3-us-west-2.amazonaws.com/` |

Render sets `PORT` for you. After deploy, note the URL, e.g. `https://blink-backend-xxxx.onrender.com`.

Confirm: `https://YOUR-BACKEND.onrender.com/api/stakeholder-roles` returns the YAML people.

### 2. Frontend — Static Site

`blink_demo` is **not** inside the `blink-backend` GitHub repo. Do not set Root Directory to `blink_demo` on the backend service.

1. Push `blink_demo` to its own GitHub repo (or a monorepo that actually contains that folder).
2. **New → Static Site** → that frontend repo.
3. Root Directory: **leave blank** if the repo root is `blink_demo`.
4. Build: `npm install && npm run build`
5. Publish: `dist`
6. Environment (**build-time**, then rebuild):

| Key | Value |
| --- | --- |
| `VITE_API_URL` | `https://blink-backend-af7x.onrender.com/api` |

On the **backend** service set:

`BLINK_CORS_ORIGINS=http://localhost:5173,https://YOUR-FRONTEND.onrender.com`

### Local vs Render

| | Frontend | Backend |
| --- | --- | --- |
| Local | http://localhost:5173 | http://localhost:8090 |
| Render | `*.onrender.com` static site | Docker web service |

## Render Postgres

Use the **External Database URL** from the Render dashboard when this API runs on your machine.

The Go API accepts a PostgreSQL connection URL directly.

Example:

```
DATABASE_URL=postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=require
```

The Go API runs its tracked database migrations at startup. Do not use the legacy Java `src/main/resources` schema or Spring/Hibernate environment variables.

## Stakeholder emails and Jira

**Gmail OAuth (recommended on Render)** — one mailbox sends login OTPs and stakeholder mail over HTTPS. Sign-in codes are sent only to `@talentserv.co.in`.

1. In [Google Cloud Console](https://console.cloud.google.com/) create (or pick) a project, enable **Gmail API**, and configure the OAuth consent screen.
2. Credentials → **OAuth client ID** → Web application. Add these authorized redirect URIs:
   - `http://localhost:8090/api/auth/gmail/oauth/callback`
   - `https://YOUR-BACKEND.onrender.com/api/auth/gmail/oauth/callback`
3. Set `BLINK_GMAIL_CLIENT_ID` and `BLINK_GMAIL_CLIENT_SECRET` on the API (local `.env` and Render). Restart.
4. While signed into the sending Gmail account, open `https://YOUR-BACKEND.onrender.com/api/auth/gmail/oauth/url` (or `http://localhost:8090/api/auth/gmail/oauth/url`).
5. Copy `BLINK_GMAIL_FROM` and `BLINK_GMAIL_REFRESH_TOKEN` from the success page into Render env (and local `.env` if you test mail locally). Restart again.
6. Set `BLINK_OTP_REVEAL=false` so production actually emails the code instead of showing it.

Testing-mode Google apps issue refresh tokens that expire after 7 days. Publish the OAuth app (or keep the Gmail user as a test user and reconnect when Google expires the token).

**Email** — `POST /api/stakeholder-questions/send` groups questions by `recipient_email` and sends **one message per person**. Gmail OAuth is used when configured; otherwise SMTP; otherwise outbox files.

| Env | Purpose |
| --- | --- |
| `BLINK_GMAIL_CLIENT_ID` / `BLINK_GMAIL_CLIENT_SECRET` | Google OAuth client for the sending mailbox |
| `BLINK_GMAIL_REFRESH_TOKEN` | Long-lived token from the one-time consent |
| `BLINK_GMAIL_FROM` | The Gmail address that consented |
| `BLINK_GMAIL_REDIRECT_URI` | Optional override for the OAuth callback |
| `BLINK_LOGIN_ALLOWED_DOMAIN` | OTP recipients must be `local@this-domain` (default `talentserv.co.in`) |
| `BLINK_SMTP_HOST` | Fallback SMTP when Gmail OAuth is unset |
| `BLINK_SMTP_PORT` | Default `587` |
| `BLINK_SMTP_USERNAME` / `BLINK_SMTP_PASSWORD` | Optional SMTP auth |
| `BLINK_SMTP_FROM` | SMTP From address |
| `BLINK_SMTP_START_TLS` | Default `true` |
| `BLINK_SMTP_OUTBOX_DIR` | When neither Gmail nor SMTP is set, write `.txt` files here (default `.blink-outbox`) |

**Jira** — Blink may read connected Jira data. Creating issues, comments, transitions, and deletions are manual provider actions; the hosted API rejects those writes.

## API

| Method | Path | Used by |
| --- | --- | --- |
| GET | `/api/stakeholder-roles` | Role dropdown |
| POST | `/api/projects` | Save & Continue (first time) |
| PUT | `/api/projects/{id}` | Save & Continue (after going back) |
| GET | `/api/projects/{id}` | Reload |
| POST | `/api/projects/{id}/download` | Download workspace zip. Includes `.cursor/mcp.json` (portable `npx`), `.cursor/mcp.windows.json` (`npx.cmd`), `.cursor/mcp.unix.json`, `.cursor/MCP_SETUP.md`, and `automation_sdlc/.env.mcp.example`. Secrets use `${env:...}` only. Form field `mcpProvider` repeats connected ids (`github`, `jira`, `confluence`). |
| POST | `/api/grooming/clarify` | Hosted `/clarify-requirement` discovery |
| POST | `/api/projects/{id}/setup` | Hosted `/setup-new-workspace` apply |
| GET | `/api/auth/gmail/oauth/url` | One-time Gmail mailbox consent (redirects to Google) |
| GET | `/api/auth/gmail/oauth/callback` | Stores nothing; shows `BLINK_GMAIL_REFRESH_TOKEN` to copy into env |
| GET | `/actuator/health` | Health |

## Table mapping

| Render table | Used for |
| --- | --- |
| `users` | Demo operator for `created_by` / `updated_by` |
| `project` | Name, description, type, code, status |
| `stakeholder_roles` | Role catalog |
| `stakeholder` | People assigned on screen 2 (created by the API) |