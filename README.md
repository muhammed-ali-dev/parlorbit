# Parlorbit

Private Codenames nights with friends. Create a House, share an invite, approve your group, and play with optional voice and video.

**Go · SQLite · WebSockets · React · TypeScript · LiveKit**

## How it came together

- Built guest sessions, private Houses, and shared rooms with the backend owning membership and game state.
- Added reconnect recovery, host transfer, bounded socket queues, and room-scoped calls.
- Refined the invite flow and responsive UI, then verified two-browser game nights, audio/video, and database recovery with browser tests and Go race tests.

Mutation Lab is a secondary multiplayer experiment where players suggest changes between rounds.

## Fork and run

Fork this repository on GitHub, then replace YOUR_USERNAME below with your username.

Requires Git, Go 1.26+, Node.js 22+, and npm.

```sh
git clone https://github.com/YOUR_USERNAME/parlorbit.git
cd parlorbit
npm ci
npm run build
go run ./cmd/roomcade
```

Open **http://localhost:8080**. Create a House and invite a friend using a separate browser profile. Local data is saved in SQLite.

Voice/video needs LiveKit configuration; Mutation Lab works with local rules without an AI key. See [call setup](MEDIA.md) or [hosting your own instance](HOSTING.md). Environment variables are read directly; the server does not automatically load .env.
