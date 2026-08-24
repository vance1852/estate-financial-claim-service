CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('claimant','case_officer','supervisor')),
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    created_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
);
CREATE INDEX idx_sessions_user_active ON sessions(user_id, expires_at, revoked_at);

CREATE TABLE estate_cases (
    id TEXT PRIMARY KEY,
    reference TEXT NOT NULL UNIQUE,
    deceased_name TEXT NOT NULL,
    deceased_id_hash TEXT NOT NULL,
    deceased_id_masked TEXT NOT NULL,
    jurisdiction TEXT NOT NULL,
    claimant_user_id TEXT NOT NULL REFERENCES users(id),
    status TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    submitted_at TEXT,
    inquiry_completed_at TEXT,
    closed_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_cases_claimant_status ON estate_cases(claimant_user_id, status, created_at DESC);
CREATE INDEX idx_cases_deceased ON estate_cases(deceased_id_hash, status);

CREATE TABLE parties (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    identity_hash TEXT NOT NULL UNIQUE,
    identity_masked TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE case_parties (
    case_id TEXT NOT NULL REFERENCES estate_cases(id) ON DELETE CASCADE,
    party_id TEXT NOT NULL REFERENCES parties(id),
    relation TEXT NOT NULL,
    verified INTEGER NOT NULL DEFAULT 0 CHECK (verified IN (0,1)),
    verified_by TEXT REFERENCES users(id),
    verified_at TEXT,
    PRIMARY KEY(case_id, party_id)
);

CREATE TABLE documents (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES estate_cases(id) ON DELETE CASCADE,
    party_id TEXT REFERENCES parties(id),
    kind TEXT NOT NULL,
    storage_key TEXT NOT NULL UNIQUE,
    checksum TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','accepted','rejected')),
    reviewed_by TEXT REFERENCES users(id),
    reviewed_at TEXT,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_documents_case_status ON documents(case_id, status, kind);

CREATE TABLE institutions (
    id TEXT PRIMARY KEY,
    code TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('bank','insurer')),
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE inquiries (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES estate_cases(id) ON DELETE CASCADE,
    institution_id TEXT NOT NULL REFERENCES institutions(id),
    request_key TEXT NOT NULL,
    status TEXT NOT NULL,
    external_ref TEXT NOT NULL DEFAULT '',
    expected_parts INTEGER NOT NULL DEFAULT 1 CHECK (expected_parts > 0),
    received_parts INTEGER NOT NULL DEFAULT 0 CHECK (received_parts >= 0),
    version INTEGER NOT NULL DEFAULT 1,
    dispatched_at TEXT,
    completed_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(case_id, institution_id, request_key)
);
CREATE INDEX idx_inquiries_case_status ON inquiries(case_id, status, updated_at);

CREATE TABLE inquiry_results (
    id TEXT PRIMARY KEY,
    inquiry_id TEXT NOT NULL REFERENCES inquiries(id) ON DELETE CASCADE,
    part_key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    received_at TEXT NOT NULL,
    UNIQUE(inquiry_id, part_key)
);

CREATE TABLE financial_accounts (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES estate_cases(id) ON DELETE CASCADE,
    institution_id TEXT NOT NULL REFERENCES institutions(id),
    inquiry_id TEXT NOT NULL REFERENCES inquiries(id),
    external_hash TEXT NOT NULL,
    kind TEXT NOT NULL,
    currency TEXT NOT NULL,
    balance_minor INTEGER NOT NULL CHECK (balance_minor >= 0),
    restricted INTEGER NOT NULL DEFAULT 0 CHECK (restricted IN (0,1)),
    restriction_note TEXT NOT NULL DEFAULT '',
    reserved_claim_id TEXT,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(case_id, institution_id, external_hash)
);
CREATE INDEX idx_accounts_case_eligibility ON financial_accounts(case_id, kind, currency, restricted, reserved_claim_id);

CREATE TABLE claims (
    id TEXT PRIMARY KEY,
    case_id TEXT NOT NULL REFERENCES estate_cases(id),
    claimant_user_id TEXT NOT NULL REFERENCES users(id),
    status TEXT NOT NULL,
    total_minor INTEGER NOT NULL CHECK (total_minor >= 0),
    currency TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    approved_by TEXT REFERENCES users(id),
    approved_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_claims_case_status ON claims(case_id, status, created_at DESC);

CREATE TABLE claim_accounts (
    claim_id TEXT NOT NULL REFERENCES claims(id) ON DELETE CASCADE,
    account_id TEXT NOT NULL REFERENCES financial_accounts(id),
    amount_minor INTEGER NOT NULL CHECK (amount_minor > 0),
    PRIMARY KEY(claim_id, account_id)
);

CREATE TABLE payouts (
    id TEXT PRIMARY KEY,
    claim_id TEXT NOT NULL UNIQUE REFERENCES claims(id),
    idempotency_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL,
    amount_minor INTEGER NOT NULL CHECK (amount_minor > 0),
    currency TEXT NOT NULL,
    provider_ref TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE idempotency_keys (
    scope TEXT NOT NULL,
    key TEXT NOT NULL,
    actor_id TEXT NOT NULL REFERENCES users(id),
    method TEXT NOT NULL,
    route TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status_code INTEGER NOT NULL,
    response_body BLOB NOT NULL,
    resource_id TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY(scope, key)
);

CREATE TABLE worker_jobs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    payload BLOB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','running','completed','failed')),
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
    available_at TEXT NOT NULL,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(kind, resource_id)
);
CREATE INDEX idx_jobs_claim ON worker_jobs(status, available_at, lease_until);

CREATE TABLE audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_id TEXT NOT NULL,
    action TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id TEXT NOT NULL,
    result TEXT NOT NULL,
    request_id TEXT NOT NULL,
    details_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_audit_object ON audit_events(object_type, object_id, created_at DESC);
CREATE INDEX idx_audit_request ON audit_events(request_id);
