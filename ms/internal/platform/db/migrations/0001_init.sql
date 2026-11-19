-- PFF schema v1.
--
-- Layout (Kimball star schema, SCD type 1 dimensions, on TimescaleDB):
--
--   bank_transaction   persistent GetOrSet cache of the banking API (hypertable)
--   fact_transaction   atomic fact: one row per (transaction, metric domain) (hypertable)
--   fact_metric        aggregate fact: PK (user, metric domain, date range, metric class)
--   dim_metric_domain  dimension: metric domains
--   dim_metric_class   dimension: PK (metric domain, metric class)
--   dim_account        dimension: bank accounts
--
-- SCD type 1: dimension attributes are overwritten in place, no history kept.

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- =============================================================================
-- User management: deny-by-default whitelist
-- =============================================================================

-- Only enabled e-mails listed here may use PFF. Rows with source = 'file' are
-- kept in sync with allowed-emails.txt at startup; 'manual' rows are managed
-- directly in the database.
CREATE TABLE user_whitelist (
    email      TEXT PRIMARY KEY CHECK (email = lower(email) AND position('@' IN email) > 1),
    source     TEXT NOT NULL CHECK (source IN ('file', 'manual')),
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE app_user (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email             TEXT NOT NULL UNIQUE CHECK (email = lower(email)),
    -- Informational: users are keyed by their whitelisted e-mail.
    firebase_uid      TEXT NOT NULL,
    display_name      TEXT,
    email_mfa_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    -- Bumped whenever the user's facts change; fact_metric rows computed at an
    -- older version are stale.
    data_version      BIGINT NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- E-mail one-time codes (second factor managed by the backend; Firebase covers
-- TOTP and SMS natively).
CREATE TABLE mfa_email_challenge (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    purpose     TEXT NOT NULL CHECK (purpose IN ('login', 'enroll')),
    code_hash   BYTEA NOT NULL,
    auth_time   TIMESTAMPTZ NOT NULL,
    attempts    INT NOT NULL DEFAULT 0,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX mfa_email_challenge_user_idx ON mfa_email_challenge (user_id, created_at DESC);

-- A sign-in session (identified by the Firebase auth_time) that has passed the
-- e-mail second factor.
CREATE TABLE mfa_session (
    user_id     UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    auth_time   TIMESTAMPTZ NOT NULL,
    method      TEXT NOT NULL,
    verified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, auth_time)
);

-- =============================================================================
-- Bank connections
-- =============================================================================

CREATE TABLE bank_connection (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id              UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    provider             TEXT NOT NULL,
    aspsp_name           TEXT NOT NULL,
    aspsp_country        TEXT NOT NULL,
    psu_type             TEXT NOT NULL DEFAULT 'personal',
    status               TEXT NOT NULL CHECK (status IN ('pending', 'active', 'expired', 'revoked', 'failed')),
    -- SHA-256 of the OAuth-style state parameter; cleared once consumed.
    auth_state_hash      BYTEA UNIQUE,
    -- Provider session id, AES-256-GCM encrypted with PFF_SECRET_KEY.
    session_enc          BYTEA,
    required_psu_headers TEXT[] NOT NULL DEFAULT '{}',
    valid_until          TIMESTAMPTZ,
    expiry_notified      BOOLEAN NOT NULL DEFAULT FALSE,
    last_error           TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX bank_connection_user_idx ON bank_connection (user_id);

-- =============================================================================
-- Dimensions (SCD type 1)
-- =============================================================================

CREATE TABLE dim_metric_domain (
    metric_domain TEXT PRIMARY KEY,
    label         TEXT NOT NULL,
    description   TEXT NOT NULL,
    sort_order    INT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE dim_metric_class (
    metric_domain TEXT NOT NULL REFERENCES dim_metric_domain (metric_domain),
    metric_class  TEXT NOT NULL,
    label         TEXT NOT NULL,
    sort_order    INT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (metric_domain, metric_class)
);

CREATE TABLE dim_account (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    connection_id       UUID REFERENCES bank_connection (id) ON DELETE SET NULL,
    provider_uid        TEXT,
    identification_hash TEXT NOT NULL,
    iban                TEXT,
    name                TEXT,
    currency            TEXT,
    cash_account_type   TEXT,
    product             TEXT,
    balance_amount      NUMERIC(20, 4),
    balance_currency    TEXT,
    balance_type        TEXT,
    balance_at          TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, identification_hash)
);

-- =============================================================================
-- GetOrSet cache of the banking API
-- =============================================================================

CREATE TABLE bank_transaction (
    id                  UUID NOT NULL DEFAULT gen_random_uuid(),
    user_id             UUID NOT NULL,
    account_id          UUID NOT NULL,
    booked_on           DATE NOT NULL,
    -- Stable de-duplication key of the transaction within its account and day.
    fingerprint         TEXT NOT NULL,
    entry_reference     TEXT,
    value_date          DATE,
    transaction_date    DATE,
    amount              NUMERIC(20, 4) NOT NULL CHECK (amount >= 0),
    currency            TEXT NOT NULL,
    direction           TEXT NOT NULL CHECK (direction IN ('CRDT', 'DBIT')),
    counterparty_name   TEXT,
    counterparty_iban   TEXT,
    remittance          TEXT,
    bank_tx_code        TEXT,
    bank_tx_sub_code    TEXT,
    bank_tx_description TEXT,
    mcc                 TEXT,
    balance_after       NUMERIC(20, 4),
    raw                 JSONB,
    fetched_at          TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (account_id, booked_on, fingerprint)
);
SELECT create_hypertable('bank_transaction', by_range('booked_on', INTERVAL '3 months'));
CREATE UNIQUE INDEX bank_transaction_id_idx ON bank_transaction (id, booked_on);
CREATE INDEX bank_transaction_user_idx ON bank_transaction (user_id, booked_on DESC);

-- Which days of an account have been fetched from the bank, and when.
CREATE TABLE sync_coverage (
    id           BIGSERIAL PRIMARY KEY,
    account_id   UUID NOT NULL REFERENCES dim_account (id) ON DELETE CASCADE,
    covered_from DATE NOT NULL,
    covered_to   DATE NOT NULL,
    fetched_at   TIMESTAMPTZ NOT NULL,
    CHECK (covered_from <= covered_to)
);
CREATE INDEX sync_coverage_account_idx ON sync_coverage (account_id, covered_from, covered_to);

-- Bank calls per account and day, to respect the PSD2 limit on fetches made
-- while the user is not present (typically 4 per day).
CREATE TABLE bank_fetch_log (
    account_id         UUID NOT NULL REFERENCES dim_account (id) ON DELETE CASCADE,
    day                DATE NOT NULL,
    background_fetches INT NOT NULL DEFAULT 0,
    user_fetches       INT NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id, day)
);

-- User corrections of the rule-based spending category (SCD type 1).
CREATE TABLE transaction_category_override (
    account_id    UUID NOT NULL REFERENCES dim_account (id) ON DELETE CASCADE,
    booked_on     DATE NOT NULL,
    fingerprint   TEXT NOT NULL,
    user_id       UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    metric_domain TEXT NOT NULL DEFAULT 'spending_category' CHECK (metric_domain = 'spending_category'),
    metric_class  TEXT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, booked_on, fingerprint),
    FOREIGN KEY (metric_domain, metric_class) REFERENCES dim_metric_class (metric_domain, metric_class)
);

-- =============================================================================
-- Facts
-- =============================================================================

-- Atomic fact: a transaction's amount within one metric domain and class.
CREATE TABLE fact_transaction (
    user_id        UUID NOT NULL,
    transaction_id UUID NOT NULL,
    account_id     UUID NOT NULL,
    booked_on      DATE NOT NULL,
    metric_domain  TEXT NOT NULL,
    metric_class   TEXT NOT NULL,
    amount         NUMERIC(20, 4) NOT NULL,
    currency       TEXT NOT NULL,
    PRIMARY KEY (transaction_id, metric_domain, booked_on),
    FOREIGN KEY (metric_domain, metric_class) REFERENCES dim_metric_class (metric_domain, metric_class)
);
SELECT create_hypertable('fact_transaction', by_range('booked_on', INTERVAL '3 months'));
CREATE INDEX fact_transaction_user_domain_idx ON fact_transaction (user_id, metric_domain, booked_on DESC);
CREATE INDEX fact_transaction_account_idx ON fact_transaction (account_id, booked_on);

-- Aggregate fact: metrics of one metric class over one date range. Computed on
-- demand (GetOrSet) and valid only while data_version matches app_user.
CREATE TABLE fact_metric (
    user_id       UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    metric_domain TEXT NOT NULL,
    range_from    DATE NOT NULL,
    range_to      DATE NOT NULL,
    metric_class  TEXT NOT NULL,
    currency      TEXT NOT NULL,
    tx_count      BIGINT NOT NULL,
    total         NUMERIC(20, 4) NOT NULL,
    mean          NUMERIC(20, 4) NOT NULL,
    max_amount    NUMERIC(20, 4) NOT NULL,
    min_amount    NUMERIC(20, 4) NOT NULL,
    data_version  BIGINT NOT NULL,
    computed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, metric_domain, range_from, range_to, metric_class),
    FOREIGN KEY (metric_domain, metric_class) REFERENCES dim_metric_class (metric_domain, metric_class),
    CHECK (range_from <= range_to)
);

-- =============================================================================
-- Budgets and notifications
-- =============================================================================

CREATE TABLE budget (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    metric_domain TEXT NOT NULL REFERENCES dim_metric_domain (metric_domain),
    metric_class  TEXT,
    range_code    TEXT NOT NULL CHECK (range_code IN ('1d', '1w', '2w', '1m', '3m', 'custom')),
    custom_from   DATE,
    custom_to     DATE,
    limit_amount  NUMERIC(20, 2) NOT NULL CHECK (limit_amount > 0),
    currency      TEXT NOT NULL,
    warn_percent  INT NOT NULL DEFAULT 80 CHECK (warn_percent BETWEEN 1 AND 100),
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (metric_domain, metric_class) REFERENCES dim_metric_class (metric_domain, metric_class),
    CHECK ((range_code = 'custom') = (custom_from IS NOT NULL AND custom_to IS NOT NULL)),
    CHECK (custom_from IS NULL OR custom_from <= custom_to)
);
CREATE INDEX budget_user_idx ON budget (user_id);

-- One alert per budget, period and level, so users are not notified twice.
CREATE TABLE budget_alert (
    budget_id   UUID NOT NULL REFERENCES budget (id) ON DELETE CASCADE,
    period_from DATE NOT NULL,
    level       TEXT NOT NULL CHECK (level IN ('warning', 'exceeded')),
    notified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (budget_id, period_from, level)
);

CREATE TABLE push_device (
    token        TEXT PRIMARY KEY,
    user_id      UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    user_agent   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX push_device_user_idx ON push_device (user_id);

CREATE TABLE notification (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL,
    link       TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at    TIMESTAMPTZ
);
CREATE INDEX notification_user_idx ON notification (user_id, created_at DESC);
