import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { firstValueFrom } from 'rxjs';
import { Donation, LogEntry, Pool } from './models';

export interface NewPool {
  code: string;
  sponsor: string;
  currency: string;
  cause: string;
  openingCents: number;
}

@Injectable({ providedIn: 'root' })
export class ApiService {
  private readonly http = inject(HttpClient);

  pools = () => firstValueFrom(this.http.get<Pool[]>('/api/pools'));
  openPool = (body: NewPool) => firstValueFrom(this.http.post<Pool>('/api/pools', body));

  donations = (poolCode: string) =>
    firstValueFrom(this.http.get<Donation[]>('/api/donations', { params: { poolCode } }));
  book = (body: { poolCode: string; reference: string; amountCents: number }) =>
    firstValueFrom(this.http.post<Donation>('/api/donations', body));
  refund = (id: string) =>
    firstValueFrom(this.http.post<Donation>(`/api/donations/${id}/refund`, {}));
  events = (id: string) => firstValueFrom(this.http.get<LogEntry[]>(`/api/donations/${id}/events`));
}
