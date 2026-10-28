import { test, expect } from '@playwright/test';
import { ADMIN, adminToken, signIn } from './support';

test('open a pool, send a donation, see the receipt, refund it', async ({ page }) => {
  const run = Date.now().toString(36).toUpperCase();
  const code = `E2E-${run}`;
  const reference = `PAY-${run}`;

  await signIn(page, ADMIN);
  await page.getByLabel('Pool code').fill(code);
  await page.getByLabel('Pool size (cents)').fill('100000');
  await page.getByRole('button', { name: 'Open pool' }).click();

  const remaining = page.getByTestId('selected-remaining');
  await expect(page.getByRole('heading', { name: `Donate to ${code}` })).toBeVisible();
  await expect(remaining).toHaveText('$1,000.00');

  await page.getByLabel('Donation reference').fill(reference);
  await page.getByLabel('Amount (cents)').fill('30000');
  await page.getByRole('button', { name: 'Donate' }).click();

  const row = page.locator(`tr[data-reference="${reference}"]`);
  await expect(row.getByTestId('status')).toHaveText('Completed', { timeout: 20_000 });
  await expect(row.getByTestId('receipt')).toHaveText(/RC-[0-9A-F]{12}/);
  // The pool matches the whole $300.00 (the fee is the donor's, not the pool's), leaving $700.00.
  await expect(remaining).toHaveText('$700.00');

  // Nothing is kept in the browser: a fresh load reads the same state from the API.
  // Nothing sensitive is stored either: the refresh cookie is HttpOnly, so a reload signs back in through it.
  expect(await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))).toBe(
    '{}',
  );
  await page.reload();
  await expect(page.getByTestId('whoami')).toContainText(ADMIN.email);
  await page.getByRole('button', { name: new RegExp(`^${code}`) }).click();
  const reloaded = page.locator(`tr[data-reference="${reference}"]`);
  await expect(reloaded.getByTestId('status')).toHaveText('Completed');
  await expect(page.getByTestId('selected-remaining')).toHaveText('$700.00');

  await reloaded.getByRole('button', { name: 'Refund' }).click();
  await expect(reloaded.getByTestId('status')).toHaveText('Refunded');
  await expect(page.getByTestId('selected-remaining')).toHaveText('$1,000.00');
  // The issued receipt is not rolled back.
  await expect(reloaded.getByTestId('receipt')).toHaveText(/RC-[0-9A-F]{12}/);
  const headers = { Authorization: `Bearer ${await adminToken(page.request)}` };
  const donations = await page.request.get(`/api/donations?poolCode=${code}`, { headers });
  const [donation] = await donations.json();
  const receipt = await page.request.get(`/api/receipts/${donation.id}`, { headers });
  expect((await receipt.json()).status).toBe('Issued');
});
