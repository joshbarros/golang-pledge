CREATE TABLE donations.donations (
    id                uuid PRIMARY KEY,
    owner_id          uuid        NOT NULL,
    pool_id           uuid        NOT NULL,
    pool_code         text        NOT NULL,
    reference         text        NOT NULL CHECK (char_length(reference) <= 40),
    amount_cents      bigint      NOT NULL,
    fee_cents         bigint      NOT NULL,
    matched_cents     bigint      NOT NULL DEFAULT 0,
    status            text        NOT NULL,
    reason            text,
    receipt_number    text,
    created_at        timestamptz NOT NULL,
    updated_at        timestamptz NOT NULL,
    match_released_at timestamptz,
    UNIQUE (pool_id, reference)
);
CREATE INDEX donations_owner_id ON donations.donations (owner_id);
CREATE INDEX donations_created_at ON donations.donations (created_at DESC);

CREATE TABLE donations.outbox_messages (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id  uuid        NOT NULL,
    module      text        NOT NULL,
    routing_key text        NOT NULL,
    key         text        NOT NULL,
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL,
    sent_at     timestamptz
);
CREATE INDEX donations_outbox_unsent ON donations.outbox_messages (id) WHERE sent_at IS NULL;

CREATE TABLE donations.inbox_messages (
    consumer   text        NOT NULL,
    message_id uuid        NOT NULL,
    claimed_at timestamptz NOT NULL,
    PRIMARY KEY (consumer, message_id)
);
