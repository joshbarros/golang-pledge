import { test, expect, Page } from '@playwright/test';
import { ADMIN, signIn } from './support';

const run = () => Date.now().toString(36).toUpperCase() + Math.floor(Math.random() * 1000);

async function openPool(page: Page, code: string, opening: number, shown: string) {
  await signIn(page, ADMIN);
  await page.getByLabel('Pool code').fill(code);
  await page.getByLabel('Pool size (cents)').fill(String(opening));
  await page.getByRole('button', { name: 'Open pool' }).click();
  await expect(page.getByRole('heading', { name: `Donate to ${code}` })).toBeVisible();
  await expect(page.getByTestId('selected-remaining')).toHaveText(shown);
}

async function submit(page: Page, reference: string, quantity: number) {
  await page.getByLabel('Donation reference').fill(reference);
  await page.getByLabel('Amount (cents)', { exact: true }).fill(String(quantity));
  await page.getByRole('button', { name: 'Donate' }).click();
}

const row = (page: Page, reference: string) => page.locator(`tr[data-reference="${reference}"]`);

test('the API refuses bad input and the page says why', async ({ page }) => {
  const id = run();
  await openPool(page, `UI-ERR-${id}`, 100000, '$1,000.00');

  await submit(page, `PAY-${id}`, 5000001);
  await expect(page.getByRole('alert')).toContainText('at most 5000000 cents');

  await submit(page, '   ', 1000);
  await expect(page.getByRole('alert')).toContainText(/reference is required/i);

  await submit(page, `PAY-${id}`, 1000);
  await expect(row(page, `PAY-${id}`).getByTestId('status')).toHaveText('Completed', {
    timeout: 20_000,
  });
  await submit(page, `PAY-${id}`, 1000);
  await expect(page.getByRole('alert')).toContainText('already');
  await expect(page.locator('tbody tr[data-reference]')).toHaveCount(1);
});

test('a poison reference is rejected, gets no document, and the amount comes back', async ({
  page,
}) => {
  const id = run();
  await openPool(page, `UI-POI-${id}`, 50000, '$500.00');

  await submit(page, `PAY-POISON-${id}`, 20000);
  const poisoned = row(page, `PAY-POISON-${id}`);
  await expect(poisoned.getByTestId('status')).toHaveText('Rejected', { timeout: 20_000 });
  await expect(poisoned).toContainText('poison');
  await expect(poisoned.getByTestId('receipt')).toHaveText('—');
  await expect(poisoned.getByRole('button', { name: 'Refund' })).toHaveCount(0);
  await expect(page.getByTestId('selected-remaining')).toHaveText('$500.00');
});

test('a donation bigger than what is left in the pool is matched in part, never beyond the pool', async ({
  page,
}) => {
  const id = run();
  await openPool(page, `UI-LAST-${id}`, 10100, '$101.00');

  await submit(page, `PAY-A-${id}`, 10000);
  await expect(row(page, `PAY-A-${id}`).getByTestId('status')).toHaveText('Completed', {
    timeout: 20_000,
  });
  await submit(page, `PAY-B-${id}`, 10000);
  await expect(row(page, `PAY-B-${id}`).getByTestId('status')).toHaveText('Completed', {
    timeout: 20_000,
  });

  await expect(row(page, `PAY-A-${id}`).getByTestId('matched')).toHaveText('$100.00');
  await expect(row(page, `PAY-B-${id}`).getByTestId('matched')).toHaveText('$1.00'); // 10000 asked, 100 left
  await expect(page.getByTestId('selected-remaining')).toHaveText('$0.00');

  // The donor's refund gives back only what was actually matched.
  await row(page, `PAY-B-${id}`).getByRole('button', { name: 'Refund' }).click();
  await expect(row(page, `PAY-B-${id}`).getByTestId('status')).toHaveText('Refunded', {
    timeout: 20_000,
  });
  await expect(page.getByTestId('selected-remaining')).toHaveText('$1.00');
});

test('the event log shows the Kafka steps and the refund keeps the document', async ({ page }) => {
  const id = run();
  await openPool(page, `UI-LOG-${id}`, 40000, '$400.00');

  await submit(page, `PAY-${id}`, 20000);
  const mine = row(page, `PAY-${id}`);
  await expect(mine.getByTestId('status')).toHaveText('Completed', { timeout: 20_000 });

  await mine.getByRole('button', { name: 'Event log' }).click();
  const log = page.getByRole('region', { name: 'Kafka event log' });
  await expect(log).toContainText('match.reserve', { timeout: 20_000 });
  await expect(log).toContainText('match.held');
  await expect(log).toContainText('receipt.issued');

  await mine.getByRole('button', { name: 'Refund' }).click();
  await expect(mine.getByTestId('status')).toHaveText('Refunded');
  await expect(mine.getByRole('button', { name: 'Refund' })).toHaveCount(0);
  await expect(mine.getByTestId('receipt')).toHaveText(/RC-[0-9A-F]{12}/);
  await expect(page.getByTestId('selected-remaining')).toHaveText('$400.00');
  await expect(log).toContainText('match.released', { timeout: 20_000 });
});
