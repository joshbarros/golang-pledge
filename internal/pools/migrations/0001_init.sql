CREATE TABLE pools.pools (
    id              uuid PRIMARY KEY,
    code            text        NOT NULL UNIQUE CHECK (char_length(code) <= 20),
    sponsor         text        NOT NULL,
    currency        text        NOT NULL,
    cause           text        NOT NULL,
    opened_at       timestamptz NOT NULL,
    opening_cents   bigint      NOT NULL,
    available_cents bigint      NOT NULL,
    created_at      timestamptz NOT NULL,
    CONSTRAINT pools_remaining CHECK (available_cents >= 0 AND available_cents <= opening_cents)
);

-- What each donation was matched: at most the donation, less when the pool was running out.
CREATE TABLE pools.match_holds (
    donation_id    uuid PRIMARY KEY,
    pool_id        uuid        NOT NULL,
    matched_cents  bigint      NOT NULL,
    status         text        NOT NULL,
    created_at     timestamptz NOT NULL,
    released_at    timestamptz,
    release_reason text
);

CREATE TABLE pools.outbox_messages (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id  uuid        NOT NULL,
    module      text        NOT NULL,
    routing_key text        NOT NULL,
    key         text        NOT NULL,
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL,
    sent_at     timestamptz
);
CREATE INDEX pools_outbox_unsent ON pools.outbox_messages (id) WHERE sent_at IS NULL;

CREATE TABLE pools.inbox_messages (
    consumer   text        NOT NULL,
    message_id uuid        NOT NULL,
    claimed_at timestamptz NOT NULL,
    PRIMARY KEY (consumer, message_id)
);
