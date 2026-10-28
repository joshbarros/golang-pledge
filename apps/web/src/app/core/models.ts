export interface Pool {
  id: string;
  code: string;
  sponsor: string;
  currency: string;
  cause: string;
  openedAt: string;
  openingCents: number;
  availableCents: number;
}

export type DonationStatus = 'Requested' | 'Held' | 'Completed' | 'Refunded' | 'Rejected';

export interface Donation {
  id: string;
  poolCode: string;
  ownerId: string;
  reference: string;
  amountCents: number;
  feeCents: number;
  matchedCents: number;
  status: DonationStatus;
  reason: string | null;
  receiptNumber: string | null;
  createdAt: string;
  updatedAt: string;
  matchReleasedAt: string | null;
}

export interface LogEntry {
  id: string;
  type: string;
  module: string;
  occurredAt: string;
}

export interface UserInfo {
  id: string;
  email: string;
  roles: string[];
}

export interface AuthResponse {
  accessToken: string;
  expiresAt: string;
  user: UserInfo;
}
