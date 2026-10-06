# Room audio and video

Calls use LiveKit alongside the House connection. Join voice starts the microphone; Camera on explicitly starts video. Cameras start off, including after a room switch. Mute persists when moving rooms. Leaving stops local devices and removes received media.

## Local development

Install the official LiveKit server for your platform. Start it in a separate terminal:

```sh
livekit-server --dev --bind 127.0.0.1
```

Start Parlorbit with the public development credentials below, using the same database path as your existing preview if you want to keep its Houses:

```sh
npm run build
LIVEKIT_URL=ws://localhost:7880 LIVEKIT_API_KEY=devkey LIVEKIT_API_SECRET=secret go run ./cmd/roomcade
```

Open http://localhost:8080 in two independent browser profiles. Create a House, invite and approve the other person, then enter the same room. Click Join voice and allow the microphone. Click Camera on and allow the camera. If playback is blocked, use Enable sound.

These development credentials are local test values. The Go server reads environment variables directly; it does not load .env. See [LiveKit local setup](https://docs.livekit.io/transport/self-hosting/local/).

## Verification

With both services running:

```sh
RUN_MEDIA_E2E=1 npm run test:e2e -- media.spec.ts --workers=1
```

The opt-in suite uses two independent Chromium sessions with simulated microphones and cameras. It verifies received audio samples and decoded video frames in both directions, remote video rendering, mute/unmute, camera stop/start, room isolation, device teardown, and recovery after denied microphone/camera permission. It passes at desktop and mobile viewport sizes. Mobile viewport coverage does not establish physical phone or Safari compatibility.

Go tests verify that only a connected member in the requested room can obtain a token, that tokens allow microphone and camera publishing with the correct room and participant name, and that the camera/microphone permissions policy is restricted to the app itself.

## Hosted release

Configure LIVEKIT_URL to the hosted secure WebSocket endpoint and set LIVEKIT_API_KEY and LIVEKIT_API_SECRET only on the backend. Serve Parlorbit over HTTPS. Test two physical devices on separate networks, including a phone, for audible speech, video, reconnection, and room switching. Hosted reachability, TURN behavior, and physical device permissions remain release checks; the local development server does not establish them.
