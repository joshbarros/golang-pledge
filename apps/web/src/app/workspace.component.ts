import { Component, DestroyRef, OnInit, computed, inject, signal } from '@angular/core';
import { DatePipe } from '@angular/common';
import { ApiService } from './core/api.service';
import { AuthService } from './core/auth.service';
import { Donation, DonationStatus, LogEntry, Pool } from './core/models';

const BADGES: Record<DonationStatus, string> = {
  Requested: 'bg-slate-100 text-slate-700 ring-slate-300',
  Held: 'bg-amber-50 text-amber-800 ring-amber-300',
  Completed: 'bg-emerald-50 text-emerald-800 ring-emerald-300',
  Refunded: 'bg-sky-50 text-sky-800 ring-sky-300',
  Rejected: 'bg-rose-50 text-rose-800 ring-rose-300',
};

@Component({
  selector: 'app-workspace',
  imports: [DatePipe],
  templateUrl: './workspace.component.html',
})
export class WorkspaceComponent implements OnInit {
  private readonly api = inject(ApiService);
  readonly auth = inject(AuthService);
  private readonly destroyRef = inject(DestroyRef);

  readonly pools = signal<Pool[]>([]);
  readonly selectedCode = signal('');
  readonly donations = signal<Donation[]>([]);
  readonly log = signal<LogEntry[]>([]);
  readonly logFor = signal<string | null>(null);
  readonly error = signal('');

  readonly code = signal('');
  readonly sponsor = signal('Acme Treasury');
  readonly currency = signal('USD');
  readonly cause = signal('Main Cause');
  readonly opening = signal('100000');

  readonly reference = signal('');
  readonly amountCents = signal('10000');

  readonly selected = computed(() => this.pools().find((s) => s.code === this.selectedCode()));

  ngOnInit(): void {
    void this.refresh();
    const timer = setInterval(() => void this.refresh(), 1000);
    this.destroyRef.onDestroy(() => clearInterval(timer));
  }

  badge(status: DonationStatus): string {
    return BADGES[status];
  }

  money(cents: number): string {
    return (cents / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' });
  }

  async refresh(): Promise<void> {
    try {
      const pools = await this.api.pools();
      this.pools.set(pools);
      if (!this.selectedCode() && pools.length > 0) this.selectedCode.set(pools[0].code);
      if (this.selectedCode()) this.donations.set(await this.api.donations(this.selectedCode()));
      const id = this.logFor();
      if (id && this.auth.isStaff()) this.log.set(await this.api.events(id));
    } catch (e: unknown) {
      // A 401 means the session is gone; the interceptor has already signed the user out.
      if ((e as { status?: number }).status !== 401) this.error.set('The API is not reachable.');
      return;
    }
    if (this.error() === 'The API is not reachable.') this.error.set('');
  }

  async select(code: string): Promise<void> {
    this.selectedCode.set(code);
    this.logFor.set(null);
    this.log.set([]);
    await this.refresh();
  }

  async openPool(): Promise<void> {
    await this.run(async () => {
      const code = this.code();
      const pool = await this.api.openPool({
        code,
        sponsor: this.sponsor(),
        currency: this.currency(),
        cause: this.cause(),
        openingCents: Number(this.opening()),
      });
      if (this.code() === code) this.code.set('');
      this.selectedCode.set(pool.code);
    });
  }

  async book(): Promise<void> {
    await this.run(async () => {
      const reference = this.reference();
      await this.api.book({
        poolCode: this.selectedCode(),
        reference,
        amountCents: Number(this.amountCents()),
      });
      if (this.reference() === reference) this.reference.set('');
    });
  }

  async refund(donation: Donation): Promise<void> {
    await this.run(() => this.api.refund(donation.id));
  }

  async showLog(donation: Donation): Promise<void> {
    this.logFor.set(donation.id);
    this.log.set([]);
    await this.refresh();
  }

  canRefund(donation: Donation): boolean {
    return donation.status === 'Held' || donation.status === 'Completed';
  }

  private async run(action: () => Promise<unknown>): Promise<void> {
    this.error.set('');
    try {
      await action();
    } catch (e: unknown) {
      const body = (e as { error?: { title?: string } }).error;
      this.error.set(body?.title ?? 'The request failed.');
    }
    await this.refresh();
  }

  field(signalRef: { set(value: string): void }, event: Event): void {
    signalRef.set((event.target as HTMLInputElement).value);
  }
}
