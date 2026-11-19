// Types mirroring the backend's JSON. Amounts are decimal strings computed by
// the backend; the client never does arithmetic on them.

export type Domain = "spending" | "spending_category" | "income" | "bank_transfer";
export type RangeCode = "1d" | "1w" | "2w" | "1m" | "3m" | "custom";

export interface PublicConfig {
  auth_mode: "firebase" | "dev";
  require_mfa: boolean;
  firebase: {
    apiKey: string;
    authDomain: string;
    projectId: string;
    appId: string;
    messagingSenderId: string;
  };
  vapid_key: string;
  push_enabled: boolean;
  bank_provider: "enablebanking" | "mock";
}

export type MfaState = "ok" | "mfa_required" | "mfa_enrollment_required";

export interface Me {
  user: { id: string; email: string; name: string; email_mfa_enabled: boolean };
  mfa: {
    state: MfaState;
    method: string;
    email_enabled: boolean;
    firebase_factor: string;
    required_by_policy: boolean;
  };
}

export interface ClassInfo {
  class: string;
  label: string;
}

export interface DomainInfo {
  domain: Domain;
  label: string;
  description: string;
  classes: ClassInfo[];
}

export interface Meta {
  domains: DomainInfo[];
  ranges: { code: RangeCode; label: string }[];
  metrics: { key: string; label: string }[];
  currency: string;
  timezone: string;
  today: string;
}

export interface Freshness {
  complete: boolean;
  warnings: string[];
}

export interface RangeDTO {
  code: RangeCode;
  from: string;
  to: string;
  days: number;
}

export interface Stats {
  count: number;
  total: string;
  mean: string;
  max: string;
  min: string;
  max_min_spread: string;
}

export interface ClassMetrics {
  class: string;
  label: string;
  stats: Stats;
  share: string;
}

export interface MetricsResult {
  domain: Domain;
  domain_label: string;
  range: RangeDTO;
  currency: string;
  overall: Stats;
  classes: ClassMetrics[];
}

export interface Allocation {
  domain: Domain;
  domain_label: string;
  range: RangeDTO;
  currency: string;
  total: string;
  items: { class: string; label: string; total: string; percentage: string }[];
}

export interface HistoryEntry {
  id: string;
  date: string;
  class: string;
  class_label: string;
  amount: string;
  currency: string;
  direction: "CRDT" | "DBIT";
  counterparty: string;
  description: string;
  account_name: string;
}

export interface HistoryPage {
  domain: Domain;
  range: RangeDTO;
  entries: HistoryEntry[];
  next_cursor?: string;
}

export interface Series {
  domain: Domain;
  range: RangeDTO;
  currency: string;
  bucket: "day" | "week" | "month";
  points: { start: string; total: string; count: number }[];
}

export interface TransactionDetail {
  id: string;
  booking_date: string;
  value_date: string | null;
  transaction_date: string | null;
  amount: string;
  currency: string;
  direction: "CRDT" | "DBIT";
  counterparty: string;
  counterparty_iban: string;
  description: string;
  entry_reference: string;
  bank_tx_code: string;
  bank_tx_sub_code: string;
  bank_tx_description: string;
  mcc: string;
  balance_after: string | null;
  account_name: string;
  account_iban: string;
  fetched_at: string;
  category_override: string;
  memberships: { domain: Domain; domain_label: string; class: string; class_label: string }[];
  raw: unknown;
}

/** A bank configured in PFF_BANKS; available is false if the provider does not list it. */
export interface Bank {
  name: string;
  country: string;
  logo?: string;
  psu_types: string[];
  beta: boolean;
  available: boolean;
}

export interface Account {
  id: string;
  name: string;
  iban: string;
  currency: string;
  cash_account_type: string;
  balance: string | null;
  balance_currency: string;
  balance_at: string | null;
}

export interface Connection {
  id: string;
  aspsp_name: string;
  aspsp_country: string;
  status: "pending" | "active" | "expired" | "revoked" | "failed";
  valid_until: string | null;
  created_at: string;
  last_error?: string;
  accounts: Account[];
}

export type BudgetState = "ok" | "warning" | "exceeded";

export interface Budget {
  id: string;
  name: string;
  domain: Domain;
  domain_label: string;
  class: string;
  class_label: string;
  range: RangeCode;
  custom_from?: string;
  custom_to?: string;
  limit: string;
  currency: string;
  warn_percent: number;
  enabled: boolean;
  period: RangeDTO;
  spent: string;
  remaining: string;
  percentage: string;
  state: BudgetState;
}

export interface BudgetInput {
  name: string;
  domain: Domain;
  class: string;
  range: RangeCode;
  custom_from?: string;
  custom_to?: string;
  limit: string;
  warn_percent: number;
  enabled: boolean;
}

export interface NotificationItem {
  id: string;
  kind: string;
  title: string;
  body: string;
  link: string;
  created_at: string;
  read_at: string | null;
}
