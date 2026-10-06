import { expect, test } from '@playwright/test';

test('creates a House and enters its fireside room', async ({ page }, testInfo) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Game night starts here.' })).toBeVisible();
  await page.getByRole('button', { name: 'Create a House' }).first().click();
  await page.getByLabel('House name').fill(`Lantern House ${testInfo.project.name}`);
  await page.getByLabel('Your name in this House').fill('Mara Bell');
  await page.getByRole('button', { name: 'Create House', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Living Room' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Bring Codenames in.' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Join voice' })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('fireside-room.png'), fullPage: true });
});

test('House overview stays separate from the room scene', async ({ page }, testInfo) => {
  await page.goto('/');
  await page.getByRole('button', { name: 'Create a House' }).first().click();
  await page.getByLabel('House name').fill(`Juniper House ${testInfo.project.name}`);
  await page.getByLabel('Your name in this House').fill('Tavi Moss');
  await page.getByRole('button', { name: 'Create House', exact: true }).click();
  await page.getByRole('button', { name: /Juniper House/ }).click();
  await page.getByRole('menuitem', { name: 'House overview' }).click();
  await expect(page.getByRole('region', { name: 'House floor plan' })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('house-overview.png'), fullPage: true });
});


test('host invites a friend, recovers from reload, and transfers after disconnect', async ({ page, browser }, testInfo) => {
  test.setTimeout(90_000);
  await page.goto('/');
  await page.getByRole('button', { name: 'Create a House', exact: true }).click();
  await page.getByLabel('House name').fill(`Friday Club ${testInfo.project.name}`);
  await page.getByLabel('Your name in this House').fill('Alex');
  await page.getByRole('button', { name: 'Create House', exact: true }).click();
  await page.getByRole('button', { name: 'Invite friends' }).click();
  const invite = await page.getByLabel('House invite').inputValue();
  await page.getByRole('button', { name: 'Close dialog' }).click();
  await page.route('https://codenames.game/r/product-preview', route => route.fulfill({ contentType: 'text/html', body: '<html><body>Shared lobby fixture</body></html>' }));
  const friend = await browser.newContext({ viewport: testInfo.project.name === 'mobile' ? { width: 390, height: 844 } : { width: 1440, height: 1000 } });
  try {
    await friend.route('https://codenames.game/r/product-preview', route => route.fulfill({ contentType: 'text/html', body: '<html><body>Shared lobby fixture</body></html>' }));
    let friendPage = await friend.newPage();
    await friendPage.goto(invite);
    await friendPage.getByLabel('Your name', { exact: true }).fill('Sam');
    await friendPage.getByRole('button', { name: 'Ask to join' }).click();
    await expect(friendPage.getByText('Waiting for approval')).toBeVisible();
    await friendPage.close();
    friendPage = await friend.newPage();
    await friendPage.goto(invite);
    await expect(friendPage.getByText('Waiting for approval')).toBeVisible();
    await page.getByRole('button', { name: 'Join requests (1)' }).click();
    await page.getByRole('button', { name: 'Let in' }).click();
    await expect(friendPage.getByRole('heading', { name: 'Living Room', exact: true })).toBeVisible();
    await expect(friendPage.getByRole('heading', { name: 'Bring Codenames in.' })).toBeVisible();
    await page.reload();
    await expect(page.getByRole('button', { name: 'Invite friends', exact: true })).toBeVisible();
    await expect(page.getByText('Sam', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Set up Codenames' }).click();
    await page.getByLabel('Private room URL').fill('https://codenames.game/r/product-preview');
    await page.getByRole('button', { name: 'Bring game into room' }).click();
    await expect(friendPage.getByRole('link', { name: /^Open Codenames(?: in another tab)?$/ })).toHaveAttribute('href', 'https://codenames.game/r/product-preview');
    await friendPage.reload();
    await expect(friendPage.getByRole('link', { name: /^Open Codenames(?: in another tab)?$/ })).toBeVisible();
    await expect(friendPage.getByRole('button', { name: 'Invite friends', exact: true })).toHaveCount(0);
    await page.close();
    await expect(friendPage.getByRole('button', { name: 'Invite friends', exact: true })).toBeVisible({ timeout: 40_000 });
  } finally { await friend.close(); }
});

test('embedded game keeps its toolbar above a full-width iframe', async ({ page }) => {
  await page.route('**/api/v1/bootstrap', async route => {
    const response = await route.fetch();
    const payload = await response.json();
    payload.data.embeddingEnabled = true;
    await route.fulfill({ response, json: payload });
  });
  // A provider fixture verifies our layout without creating third-party rooms.
  await page.route('https://codenames.game/r/layout-preview', route => route.fulfill({ contentType: 'text/html', body: '<html><body>Provider layout fixture</body></html>' }));
  await page.goto('/');
  await page.getByRole('button', { name: 'Create a House', exact: true }).click();
  await page.getByLabel('House name').fill('Layout preview');
  await page.getByLabel('Your name in this House').fill('Alex');
  await page.getByRole('button', { name: 'Create House', exact: true }).click();
  await page.getByRole('button', { name: 'Set up Codenames' }).click();
  await page.getByLabel('Private room URL').fill('https://codenames.game/r/layout-preview');
  await page.getByRole('button', { name: 'Bring game into room' }).click();
  const frame = page.getByTitle('Codenames in Living Room');
  await expect(frame).toBeVisible();
  const frameBounds = (await frame.boundingBox())!;
  const toolbarBounds = (await page.locator('.gameToolbar').boundingBox())!;
  expect(frameBounds.width).toBeGreaterThan(toolbarBounds.width - 2);
  expect(frameBounds.y).toBeGreaterThanOrEqual(toolbarBounds.y + toolbarBounds.height - 1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
