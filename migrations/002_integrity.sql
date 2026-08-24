CREATE TABLE schema_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO schema_metadata(key, value, updated_at)
VALUES ('small_claim_limit_minor', '5000000', strftime('%Y-%m-%dT%H:%M:%fZ','now'));

CREATE TRIGGER prevent_account_reservation_rewrite
BEFORE UPDATE OF reserved_claim_id ON financial_accounts
WHEN OLD.reserved_claim_id IS NOT NULL
 AND OLD.reserved_claim_id != ''
 AND NEW.reserved_claim_id != OLD.reserved_claim_id
BEGIN
    SELECT RAISE(ABORT, 'account reservation is immutable');
END;

CREATE TRIGGER prevent_confirmed_payout_reopen
BEFORE UPDATE OF status ON payouts
WHEN OLD.status = 'confirmed' AND NEW.status != 'confirmed'
BEGIN
    SELECT RAISE(ABORT, 'confirmed payout is terminal');
END;
