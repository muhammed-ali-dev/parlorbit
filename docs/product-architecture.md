# Product release architecture

Parlorbit's first release is private Codenames game nights: create a House, invite friends, approve requests, choose a room, and share one provider lobby. Guest sessions keep onboarding and operations small. Mutation Lab stays optional and experimental.

## Deployment shape

Keep one Go service serving the React build, HTTP API, House WebSockets, and optional arena SSE. Keep one SQLite database on a persistent disk. Codenames owns gameplay; LiveKit owns audio/video calls. No Redis, separate API service, or application replicas are needed for the initial bounded alpha.

Browser → Go service → SQLite
Browser ↔ House WebSocket hub
Browser → Codenames (external tab by default; iframe when enabled)
Browser ↔ LiveKit (optional audio/video)
Go service → Gemini (Mutation Lab only, with existing spend caps)

| Boundary | Owns | Must not own |
| --- | --- | --- |
| Session/access | Guest cookie, demo gate, membership checks | Codenames identity |
| Houses | Invitations, approval, rooms, durable revisions | Provider roles or scores |
| Realtime | Presence, reconnect, room transitions, host recovery | Durable membership authority |
| Game links | Validated URL, coordinator, revision | Codenames match state |
| Voice | Short-lived tokens and room revocation | House membership |
| Mutation Lab | Arena simulation, validated mutations, AI caps | Core game-night availability |

## Change order

1. Implemented: one bounded outbound queue per connection replaces sequential socket writes. A full queue disconnects the lagging client, which reconnects to a complete snapshot. Command acknowledgments and terminal events preserve FIFO order. Snapshots are built before enqueueing; socket writes occur outside command locks. Queue overflow, order, and write-failure tests pass under the race detector.
2. Replace the application-wide command lock with House-scoped ordering. Acquire locks in a documented order; do not hold them during provider network calls. Voice room transitions need a generation check before committing after revocation, so delayed requests cannot overwrite newer state.
3. Keep SQLite transactions for membership/capacity, invite rotation, and canonical game edits. Add a persistent room-level game generation to prevent stale edits across clear/recreate. Commit House creation and its idempotency record together, binding keys to request payloads.
4. Measure before changing persistence. Adopt Postgres only when concurrent writers, multiple replicas, or measured SQLite contention requires it. Multiple replicas also require shared presence ownership; changing the database alone does not solve that.

Step 1 is implemented. Steps 2–4 remain target changes; the application-wide command lock and single database connection still exist. Current architecture details remain in architecture.md.

## Release acceptance

- Two independent browsers complete create → invite → approve → join → Codenames link → reconnect. Repeat on a physical phone and production HTTPS.
- A host can see join requests, invite friends, manage a game, and return after a transient disconnect. Rejected, expired, full, and rotated invitations explain the next action.
- A slow realtime client cannot block an unrelated House. Final-slot approval races and host reconnect at the grace deadline have integration coverage.
- Backup/restore is exercised against a private restored database; readiness and error monitoring work on the hosted service.
- Codenames opens in a separate tab unless embedding is explicitly enabled. Public iframe availability still depends on provider permission and browser verification.
- Local audio/video passes two-browser simulated-device checks for join, mute, camera controls, room switch, permission recovery, and teardown. Hosted release still requires physical-device and separate-network checks, including reconnect; see ../MEDIA.md.
- Keep the existing password gate while Mutation Lab is exposed with paid AI. Disabling that gate requires a deliberate replacement for public abuse controls; invite-only Houses alone do not protect session/House creation or model costs.

## Costs and identity

Guest names use existing sessions and membership without email delivery, OAuth configuration, or recovery support. Returning access is browser-bound and is lost when the session expires or cookies are cleared. This is an onboarding choice, not a promise of permanent accounts. An invite lets a person request access again.

The baseline paid resource is the application host plus persistent storage. Voice and optional AI add their own usage. Account sign-in does not necessarily require a separate paid service, but adds engineering and operational responsibilities. Start with guests; add durable accounts when cross-device return becomes a demonstrated need.
