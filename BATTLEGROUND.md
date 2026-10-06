# Mutation battleground: interview demo

This feature extends the existing Go/SQLite/React social app. It is a 2D multiplayer arena, not an arbitrary AI game generator.

## Run

From the project directory:

```sh
npm run build
go run ./cmd/roomcade
```

Open http://localhost:8080, create or enter a House, enter a room, and click **Mutation battleground**. The arena opens in a second tab so the House and optional voice session can stay open.

For a clean demo without touching the existing database:

```sh
DATABASE_PATH=/tmp/mutation-demo.db go run ./cmd/roomcade
```

For two people, use the existing House invitation and approval flow in separate browser profiles. Each approved member must enter the same House room before opening its battleground. Copying an arena URL does not grant membership.

## 90-second walkthrough

1. Enter the battleground. Point out the provider status: local rules or configured Gemini.
2. The host starts a 25-second mutation window. Submit **ice and giant players**. The second player can submit **lava and bouncing hazards**.
3. Click **Play now** to skip the rest of the mutation timer. Collect honey-colored sparks with WASD/arrow keys; hazards subtract two points and respawn the player.
4. At 45 seconds, the server ends the round and shows scores. Mutations remain for the next round. Three rounds end the session.
5. Demonstrate host-only emergency reset. Explain that reset also invalidates a pending AI response, preventing an old generation from modifying a new session.

## Optional real AI

Set `GEMINI_API_KEY` in the backend environment and restart. Optionally set `BATTLE_AI_MODEL` (default `gemini-3.5-flash-lite`, based on current Google documentation). Never put the key in frontend code or Git.

The server batches each round's prompts, requests structured JSON from Gemini, validates preset IDs and ArenaScript programs, and hot-applies the plan. The expression interpreter runs new programs on the shared server. A fifteen-second provider timeout or invalid response falls back to local keyword rules, which the UI labels explicitly. Gemini requests send prompt text, not House member names.

Provider integration is implemented, but a live request has not been verified without credentials. Do not claim an AI-powered demo until you configure and successfully test it.

Reference used for the request format: https://ai.google.dev/gemini-api/docs/generate-content/structured-output?hl=en

## Architecture and tradeoffs

- Go simulates movement, collisions, hazards, scoring, and phase transitions at 30 ticks/second.
- Browsers send only movement intent. Position and score are never accepted from clients.
- Server-Sent Events publish room snapshots at 20 updates/second. Commands use authenticated same-origin HTTP; existing House presence uses WebSockets.
- Existing SQLite memberships authorize every command. Streams recheck membership and active room once per second.
- Input expires after 400 ms without a heartbeat. Disconnected actors stop moving and collecting sparks.
- A mutex protects each arena; generation runs outside that lock. An epoch guards against stale AI responses after reset.
- Arena state is in memory and expires after ten minutes without activity. It does not survive process restarts.
- The existing app's global command lock remains a scaling limit. No throughput or production-scale claim has been measured.
- Eight distinct mechanics are implemented. Ice/speed/size/wind/controls/hazards can combine; giant takes precedence if giant and tiny are both active.

## Resume wording you can substantiate

**Multiplayer mutation battleground — Go, React/TypeScript, SQLite, Canvas**

- Extended a private multiplayer app with an authoritative 30 Hz arena simulation and 20 Hz state streaming, including timed rounds, shared mutations, collision scoring, and host-controlled resets.
- Reused session and membership authorization for room-scoped game commands; added stale-input expiry and tested host permissions, world synchronization, and reset behavior.

After verifying a live provider call, you can add:

- Integrated Gemini structured outputs to translate collective player prompts into validated game mutations, with asynchronous resolution, request timeouts, and an explicit fallback path.

Do not claim arbitrary game generation, GPU acceleration, load benchmarks, or user growth. The existing House app's persistence/reconnect features are separate from this arena's in-memory state.

## AI-written features and multiplayer update

The current resolver also writes ArenaScript programs, rather than only choosing preset IDs. Each round makes **one** backend Gemini call combining all submitted ideas. Default model: gemini-3.5-flash-lite; override with BATTLE_AI_MODEL. The output budget is 2,048 tokens, with a 15-second timeout and keyword fallback. No AI call is made when no key or no submissions exist. Demo/fallback labels are explicit.

Set GEMINI_API_KEY in your local .env, then export its values before starting Go (the Go app does not automatically load .env):

    set -a
    . ./.env
    set +a
    go run ./cmd/roomcade

Do not commit .env. Use prompts such as “Add a portal orbiting the center that sends players to the opposite side” or “A moving bonus circle rewards players every three seconds.” Inspect returned source under **··· → Feature code**. With no key, **··· → Try a portal demo** runs a handwritten program and displays **Program demo**; this is not AI generation.

ArenaScript is a restricted expression language. A program defines zone center x/y, radius, continuous force x/y, contact points, optional teleport x/y, and a per-player cooldown. Formulas can use t (seconds), px/py (player position), score, arithmetic, and sin/cos/abs/min/max. Geometry uses only t. Each expression is <=160 bytes and <=64 AST nodes with depth <=12. There are no loops, network/file access, dynamic evaluation, or host-language functions. Positions, radius, forces, scores, and cooldowns are bounded. Non-finite evaluation skips the effect for that tick. Up to two new programs per round, six per match. They persist between rounds and reset with the match.

This supports new formula-based behaviors (moving portals, attraction fields, scoring zones), within these primitives. It does not generate arbitrary Go/JavaScript features, assets, game genres, or app rewrites. The AI can return no program for unsupported requests. A provider failure produces a visibly labeled local fallback.

Multiplayer supports the existing House limit of eight members. Invite and approve friends through the House UI; each player enters the same room and opens Mutation Lab in a separate browser profile/device. Tabs in the same profile share an identity. Arena links do not bypass membership. Server movement, scoring, feature execution, and feature positions are shared; clients only send direction input. Localhost works on the host computer; remote friends need the existing app served at a reachable URL.

Validation covers separate authenticated clients receiving the same arena, a second player's movement, generated program hot-application and persistence, two players triggering the same portal independently, cooldowns, reset, invalid arithmetic, and rejected host-language code. Provider tests use controlled mocked responses; a live Gemini call still requires your local API key.


## Owner-controlled AI spending guards

Defaults apply across every House and room sharing the application's SQLite database:

| Window | Maximum AI request attempts |
| --- | ---: |
| Minute | 2 |
| Hour | 10 |
| UTC day | 30 |
| UTC calendar month | 300 |

The server reserves the allowance transactionally before calling Gemini. Failed calls, canceled rounds, and invalid output still consume the attempt. Match resets, arena expiry, House deletion, and server restarts do not clear these counters. Database failures block AI calls. Only one AI call may be in flight per server process. There are no automatic provider retries.

Configure BATTLE_AI_MAX_MINUTE_CALLS, BATTLE_AI_MAX_HOURLY_CALLS, BATTLE_AI_MAX_DAILY_CALLS, and BATTLE_AI_MAX_MONTHLY_CALLS through the server environment. Defaults are in .env.example. Set BATTLE_AI_ENABLED=false to disable AI, or set any cap to zero; invalid cap values also disable calls. Restart after changing configuration. Existing 2,048 output-token and 180-byte per-player prompt limits remain, with an additional provider request-size bound. Clients cannot change the model, budget, or enabled flag.

Blocked rounds keep playing using local keyword rules and show “AI paused”; the options menu explains why. Local/demo rounds do not consume allowance. Prompt updates allow a burst of three then one per second; match controls allow a burst of four then one per second. Movement allows 40 requests per second with a burst of 40 (normal client input is at most 20/sec). Each identity can open at most two arena streams.

These are application request caps, not a provider-account dollar guarantee. Limits are fixed UTC calendar windows, not rolling periods. Multiple deployments with separate databases have separate allowances. Other apps using the same key are outside this guard. Deleting/replacing the database resets its counters; keep the persistent disk. Actual dollar costs depend on model pricing and tokens; provider-side account restrictions are separate.
