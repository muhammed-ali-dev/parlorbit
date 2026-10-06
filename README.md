# Parlorbit

A private place for friends to play Codenames together. Create a House, approve friends through an invite link, and move between rooms with a shared game lobby and optional audio/video calls.

**Go · SQLite · WebSockets · React / TypeScript · LiveKit**

The interesting part is keeping everyone in agreement when tabs disconnect, two people edit the same game, or the host leaves. The backend owns membership, permissions, room state, and game revisions; browsers receive snapshots of that state.

## Start with the backend

| Problem | Implementation | Code |
| --- | --- | --- |
| Two clients replace the same lobby | Game writes check the caller's expected revision inside a transaction; stale writes return a conflict. | [repository.go](internal/app/repository.go) |
| An invitation is shared beyond the intended group | An invite allows a join request. The host must approve it, and approval checks capacity inside the transaction. | [repository.go](internal/app/repository.go) |
| A browser reconnects or opens a second tab | Reconnects receive a full snapshot. The new connection replaces the old connection for that session in the House. | [hub.go](internal/app/hub.go) |
| The host loses their connection | A 30-second grace period allows recovery before authority transfers to a connected member, ordered by join time and member ID. | [hub.go](internal/app/hub.go) |
| Voice access outlives a room change | Room switching revokes the previous media identity before updating the member's room; failed revocation rejects the switch. | [voice.go](internal/app/voice.go), [hub.go](internal/app/hub.go) |

## Try it

Run the app, then open it in two separate browser profiles. Create a House in the first, send its invite to the second, and approve the request as the host. Share a Codenames lobby and switch rooms. Separate profiles matter: two tabs in the same session intentionally replace one another.

## Mutation battleground

Each House room now has a **Mutation battleground** entry point: a private, three-round Canvas arena with server-controlled movement and scoring. Up to eight House members share the same server-controlled arena. Players submit ideas between rounds; Gemini can write new ArenaScript feature programs in addition to eight existing mechanics. Programs are validated and applied to every player without restarting the arena.

The arena uses a 30 Hz Go simulation and 20 Hz Server-Sent Events snapshots. Without credentials, it explicitly uses local keyword rules. An optional Gemini resolver maps collective prompts to validated mutation plans; this is not arbitrary code or game generation. Live provider verification requires a key. AI usage is capped globally at 2 attempts/minute, 10/hour, 30/day, and 300/month, persisted in SQLite. Owner settings can lower the caps or disable AI.

See [BATTLEGROUND.md](BATTLEGROUND.md) for the demo walkthrough, provider setup, architecture, limitations, and evidence-based resume bullets.

## Password-protected demo

The Render Blueprint requires DEMO_PASSWORD (a private value of at least 12 characters) and sets DEMO_PASSWORD_REQUIRED=true. Set the secret in Render's Environment settings and deploy the updated code. It is not included in the frontend bundle or repository. Deployment has not been performed by this change.

Visitors see: “Due to current model costs, you need a password for this demo.”

The backend protects pages, assets, guest creation, House APIs, arena commands, SSE, and WebSockets. A correct password issues a signed, HttpOnly, SameSite=Strict session cookie valid for 12 hours (Secure on Render). Rotating DEMO_PASSWORD and restarting invalidates previous cookies. House membership and host approval still apply after entering the demo. The AI spending caps remain enabled.

Login attempts are persisted in SQLite: 5 per minute per server-observed source address, 20 per minute globally, and 60 per hour globally. Proxy users may share a source-address limit; client forwarding headers cannot bypass it. The gate refuses login if its counter database is unavailable. Readiness/health checks remain public; metrics and voice webhooks retain their own authentication.

For a local password preview, configure DEMO_PASSWORD in .env and export it before starting Go. Leaving it unset is allowed only when DEMO_PASSWORD_REQUIRED=false, for local development. Never share or commit the actual .env.

## Run locally

Requirements: Go 1.26+, Node 22+, and npm.

```sh
npm ci
npm run build
go run ./cmd/roomcade
```

Open `http://localhost:8080`. The default local database is `roomcade.db`. Calls display an explicit unconfigured state until the three `LIVEKIT_*` values are supplied. See [local audio/video setup](MEDIA.md).

For frontend hot reload, run the Go server and `npm run dev` in separate terminals, then open `http://localhost:5173`.

## Deploy

The Go server serves both the built frontend and the API, including WebSockets.
Deploy the full Docker service; a Vite-only Vercel deployment has no backend.

1. In [Render](https://dashboard.render.com/), choose **New → Blueprint** and connect this repository.
2. Review the resources from `render.yaml`: one 0.5c-512mb web service and a 1 GB persistent disk. These are paid resources.
3. Deploy, then open the service's `https://…onrender.com` URL. Use that URL for the app and invitation links.

The Blueprint sets the allowed origin from Render's assigned public URL and stores
SQLite at `/var/data/roomcade.db`. Audio/video calls are optional: add the three `LIVEKIT_*`
variables later. For a custom domain, update `ALLOWED_ORIGINS` to include its exact
HTTPS origin. The existing Vercel URL remains frontend-only until separately redirected
or connected to a backend.

See [private alpha hosting and recovery](HOSTING.md) for configuration, verified SQLite backups, and hosted acceptance checks.

## Verify

```sh
npm test
npm run build
CGO_ENABLED=1 go test -race ./...
npm run test:e2e
```

Playwright starts the app itself unless port 8080 already contains a compatible local Parlorbit server. Install its Chromium build once with `npx playwright install chromium`.

## Configuration

Copy `.env.example` into your preferred local environment loader. The Go process reads variables directly; it does not parse `.env` files.

- `ALLOWED_ORIGINS` is a comma-separated exact allowlist.
- `SECURE_COOKIES` must be `true` under production HTTPS.
- `METRICS_TOKEN` protects `/metrics` with a Bearer token.
- `LIVEKIT_URL`, `LIVEKIT_API_KEY`, and `LIVEKIT_API_SECRET` enable room audio/video calls and webhook verification.

## Scope and current limits

This is a single-process application with a local SQLite database. A House supports up to eight approved members and four rooms. Presence and recovery timers live in memory; membership and game state survive restarts.

The server serializes authenticated HTTP handlers and realtime commands with one shared lock. That keeps coordination straightforward at this scale, snapshot construction and voice revocation can still delay unrelated Houses. Socket writes now use bounded per-connection queues. There are no throughput or production-availability claims here.

Local audio/video integration passes two-browser tests with simulated devices. Hosted calls still require LiveKit credentials and physical-device verification. See [media setup and checks](MEDIA.md). Docker and Render configuration are included; hosted deployment is not verified here. Public Codenames embedding also requires publisher permission. Parlorbit shares provider lobby URLs and does not implement the game's rules.

## Tests and operations

The [Go tests](internal/app/repository_test.go) cover URL validation, room limits, persisted game state, stale game deletion, invitation approval, and member capacity. The [browser tests](e2e/roomcade.spec.ts) cover House creation and navigation on desktop and mobile. Disconnect recovery and multi-client races need broader integration coverage.

## Design reference

Parlorbit is the working product name. The game-night dashboard and room use neutral surfaces, muted green accents, Nunito typography, and a browsable House collection. Codenames is primary; Mutation Lab is an optional experiment. The responsive layouts keep secondary actions in menus and use compact cards and dialogs.

[Editable Figma screens and foundations](https://www.figma.com/design/ueqg10kXmMBfTNeFjMn2XB?node-id=5-2) record the earlier design pass with desktop and mobile home and arena screens, a mobile access screen, color variables and reusable controls. Node IDs are recorded in `design/figma.json`. The file title still shows an earlier provisional name because the connector cannot rename files.

The product name is provisional; trademark and domain availability have not been cleared. Existing storage keys and internal project/module names are retained for compatibility.

The current app uses the revised game-night dashboard, House collection, and Codenames-first room chooser. Figma synchronization is pending: the connector reached its Starter-plan MCP call limit after read-only discovery. See [design contract](design/PRODUCT-DESIGN.md), [sync manifest](design/product-v1.json), and [release architecture](docs/product-architecture.md). The arena and demo-access views retain the earlier styling.
