CREATE TABLE ledger_entries (
    id         text PRIMARY KEY,
    ref        text        NOT NULL UNIQUE,
    currency   char(3)     NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ledger_postings (
    id       bigserial PRIMARY KEY,
    entry_id text   NOT NULL REFERENCES ledger_entries (id),
    account  text   NOT NULL,
    amount   bigint NOT NULL CHECK (amount <> 0)
);

CREATE INDEX ledger_postings_account_idx ON ledger_postings (account);
CREATE INDEX ledger_postings_entry_idx ON ledger_postings (entry_id);

-- The application validates entries, but the database has the last word:
-- an entry that does not sum to zero cannot be committed.
CREATE FUNCTION ledger_entry_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (SELECT sum(amount) FROM ledger_postings WHERE entry_id = NEW.entry_id) <> 0 THEN
        RAISE EXCEPTION 'ledger entry % does not balance', NEW.entry_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER ledger_postings_balanced
    AFTER INSERT ON ledger_postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_entry_balanced();

-- Corrections are new entries, never edits.
CREATE FUNCTION ledger_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger is append-only';
END
$$;

CREATE TRIGGER ledger_entries_append_only
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();

CREATE TRIGGER ledger_postings_append_only
    BEFORE UPDATE OR DELETE ON ledger_postings
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
