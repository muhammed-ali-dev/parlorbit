import { expect, test, type Page } from '@playwright/test';

test.skip(process.env.RUN_MEDIA_E2E !== '1', 'Requires the local LiveKit development server and configured backend.');
test.use({ permissions: ['microphone', 'camera'], launchOptions: { args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream', '--autoplay-policy=no-user-gesture-required'] } });

function observeMedia() {
  const observed = window as unknown as { mediaPeers: RTCPeerConnection[]; mediaTracks: MediaStreamTrack[] };
  observed.mediaPeers = []; observed.mediaTracks = [];
  const OriginalPeer = window.RTCPeerConnection;
  window.RTCPeerConnection = new Proxy(OriginalPeer, { construct(target, args) { const peer = new target(...args); observed.mediaPeers.push(peer); return peer; } });
  const capture = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
  navigator.mediaDevices.getUserMedia = async constraints => { const stream = await capture(constraints); observed.mediaTracks.push(...stream.getTracks()); return stream; };
}

async function receivedMedia(page: Page) {
  return page.evaluate(async () => {
    const observed = window as unknown as { mediaPeers: RTCPeerConnection[] };
    const result = { audioBytes: 0, audioSamples: 0, videoFrames: 0 };
    for (const peer of observed.mediaPeers) {
      if (peer.connectionState === 'closed') continue;
      const reports = await peer.getStats();
      reports.forEach(report => {
        if (report.type !== 'inbound-rtp') return;
        if (report.kind === 'audio') { result.audioBytes += report.bytesReceived || 0; result.audioSamples += report.totalSamplesReceived || 0; }
        if (report.kind === 'video') result.videoFrames += report.framesDecoded || 0;
      });
    }
    return result;
  });
}

async function stoppedDevices(page: Page) {
  return page.evaluate(() => (window as unknown as { mediaTracks: MediaStreamTrack[] }).mediaTracks.every(track => track.readyState === 'ended'));
}

test('two users receive audio/video, mute, stop camera, switch rooms, and leave', async ({ page, browser }, testInfo) => {
  test.setTimeout(90_000);
  await page.addInitScript(observeMedia);
  await page.goto('/');
  await page.getByRole('button', { name: 'Create a House', exact: true }).click();
  await page.getByLabel('House name').fill(`Media check ${testInfo.project.name}`);
  await page.getByLabel('Your name in this House').fill('Alex');
  await page.getByRole('button', { name: 'Create House', exact: true }).click();
  await page.getByRole('button', { name: 'Invite friends' }).click();
  const invite = await page.getByLabel('House invite').inputValue();
  await page.getByRole('button', { name: 'Close dialog' }).click();
  const friend = await browser.newContext({ permissions: ['microphone', 'camera'], viewport: testInfo.project.name === 'mobile' ? { width: 390, height: 844 } : { width: 1440, height: 1000 } });
  try {
    await friend.addInitScript(observeMedia);
    const other = await friend.newPage();
    await other.goto(invite);
    await other.getByLabel('Your name', { exact: true }).fill('Sam');
    await other.getByRole('button', { name: 'Ask to join' }).click();
    await page.getByRole('button', { name: 'Join requests (1)' }).click();
    await page.getByRole('button', { name: 'Let in' }).click();
    await expect(other.getByRole('heading', { name: 'Living Room', exact: true })).toBeVisible();
    for (const client of [page, other]) {
      await client.getByRole('button', { name: 'Join voice', exact: true }).click();
      await expect(client.getByText('Voice connected.', { exact: true })).toBeVisible({ timeout: 20_000 });
    }
    for (const client of [page, other]) {
      await expect.poll(async () => (await receivedMedia(client)).audioSamples, { timeout: 20_000 }).toBeGreaterThan(0);
      await expect(client.locator('[data-room-audio] audio')).toHaveCount(1);
      await client.getByRole('button', { name: 'Camera on', exact: true }).click();
    }
    await expect(page.getByLabel('Sam camera')).toBeVisible();
    await expect(other.getByLabel('Alex camera')).toBeVisible();
    for (const client of [page, other]) {
      await expect.poll(async () => (await receivedMedia(client)).videoFrames, { timeout: 20_000 }).toBeGreaterThan(0);
      await expect.poll(() => client.locator('video').evaluateAll(nodes => nodes.every(node => (node as HTMLVideoElement).videoWidth > 0))).toBe(true);
    }
    await page.screenshot({ path: testInfo.outputPath('working-audio-video.png'), fullPage: true });
    await page.getByRole('button', { name: 'Mute', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Unmute', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Unmute', exact: true }).click();
    await expect(page.getByText('Voice connected.', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Camera off', exact: true }).click();
    await expect(other.getByLabel('Alex camera')).toHaveCount(0);
    await page.getByRole('button', { name: 'Camera on', exact: true }).click();
    await expect(other.getByLabel('Alex camera')).toBeVisible();
    await page.getByRole('button', { name: 'Add room', exact: true }).click();
    await page.getByLabel('Room name', { exact: true }).fill('Quiet Room');
    await page.getByRole('button', { name: 'Add room', exact: true }).click();
    await page.getByRole('button', { name: /Quiet Room/ }).click();
    await expect(page.getByRole('heading', { name: 'Quiet Room', exact: true })).toBeVisible();
    await expect(other.locator('[data-room-audio] audio')).toHaveCount(0);
    await expect(other.getByLabel('Alex camera')).toHaveCount(0);
    await expect(page.locator('video')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Camera on', exact: true })).toBeVisible();
    for (const client of [page, other]) {
      await client.getByRole('button', { name: 'Leave voice', exact: true }).click();
      await expect.poll(() => stoppedDevices(client)).toBe(true);
      await expect(client.locator('[data-room-audio] audio, .callVideos video')).toHaveCount(0);
    }
    console.log(JSON.stringify({ project: testInfo.project.name, result: 'pass', checks: ['bidirectional decoded audio', 'bidirectional decoded video', 'mute/unmute', 'camera off/on', 'room isolation', 'device teardown'] }));
  } finally { await friend.close(); }
});


test('denied microphone and camera permissions recover without losing the call', async ({ page }) => {
  await page.addInitScript(observeMedia);
  await page.addInitScript(() => {
    const flags = window as unknown as { denyMicrophone: boolean; denyCamera: boolean };
    flags.denyMicrophone = true; flags.denyCamera = true;
    const capture = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
    navigator.mediaDevices.getUserMedia = constraints => {
      if ((constraints?.audio && flags.denyMicrophone) || (constraints?.video && flags.denyCamera)) return Promise.reject(new DOMException('Permission denied for test', 'NotAllowedError'));
      return capture(constraints);
    };
  });
  await page.goto('/');
  await page.getByRole('button', { name: 'Create a House', exact: true }).click();
  await page.getByLabel('House name').fill('Permission recovery');
  await page.getByLabel('Your name in this House').fill('Alex');
  await page.getByRole('button', { name: 'Create House', exact: true }).click();
  await page.getByRole('button', { name: 'Join voice', exact: true }).click();
  await expect(page.getByText('Listening only. Allow microphone access to speak.', { exact: true })).toBeVisible({ timeout: 20_000 });
  await page.evaluate(() => { (window as unknown as { denyMicrophone: boolean }).denyMicrophone = false; });
  await page.getByRole('button', { name: 'Enable microphone', exact: true }).click();
  await expect(page.getByText('Voice connected.', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Camera on', exact: true }).click();
  await expect(page.getByRole('alert')).toContainText('Camera unavailable.');
  await expect(page.getByText('Voice connected.', { exact: true })).toBeVisible();
  await expect(page.locator('.callVideos video')).toHaveCount(0);
  await page.evaluate(() => { (window as unknown as { denyCamera: boolean }).denyCamera = false; });
  await page.getByRole('button', { name: 'Camera on', exact: true }).click();
  await expect(page.getByLabel('Your camera preview')).toBeVisible();
  await expect(page.getByRole('alert')).toHaveCount(0);
  await page.getByRole('button', { name: 'Leave voice', exact: true }).click();
  await expect.poll(() => stoppedDevices(page)).toBe(true);
  await expect(page.locator('[data-room-audio] audio, .callVideos video')).toHaveCount(0);
});
