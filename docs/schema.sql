CREATE TABLE accounts (
    id SERIAL PRIMARY KEY,
    reddit_account_id character varying(32) DEFAULT ''::character varying,
    username character varying(20) DEFAULT ''::character varying UNIQUE,
    access_token text DEFAULT '',
    refresh_token text DEFAULT '',
    token_expires_at timestamp without time zone,
    last_message_id character varying(32) DEFAULT ''::character varying,
    next_notification_check_at timestamp without time zone,
    next_stuck_notification_check_at timestamp without time zone,
    check_count integer DEFAULT 0,
    is_deleted boolean DEFAULT FALSE,
    development boolean DEFAULT FALSE,
    reddit_client_id character varying(64) NOT NULL DEFAULT '',
    reddit_client_secret character varying(128) NOT NULL DEFAULT '',
    reddit_redirect_uri character varying(255) NOT NULL DEFAULT '',
    reddit_user_agent character varying(255) NOT NULL DEFAULT ''
);

CREATE TABLE devices (
    id SERIAL PRIMARY KEY,
    apns_token character varying(100) UNIQUE,
    sandbox boolean,
    -- Delivery transport: 'apns' (default), or 'bark' for free-sideload
    -- devices that receive notifications as an HTTP POST to their Bark push
    -- URL (transport_endpoint). Mirrors migrations/000014.
    transport character varying(16) NOT NULL DEFAULT 'apns',
    transport_endpoint text NOT NULL DEFAULT ''
);

CREATE TABLE devices_accounts (
    id SERIAL PRIMARY KEY,
    account_id integer REFERENCES accounts(id) ON DELETE CASCADE,
    device_id integer REFERENCES devices(id) ON DELETE CASCADE,
    watcher_notifiable boolean DEFAULT true,
    inbox_notifiable boolean DEFAULT true,
    global_mute boolean DEFAULT false
);

CREATE UNIQUE INDEX devices_accounts_account_id_device_id_idx ON devices_accounts(account_id int4_ops,device_id int4_ops);

CREATE TABLE subreddits (
    id SERIAL PRIMARY KEY,
    subreddit_id character varying(32) DEFAULT ''::character varying UNIQUE,
    name character varying(32) DEFAULT ''::character varying,
    next_check_at timestamp without time zone
);

CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    user_id character varying(32) DEFAULT ''::character varying UNIQUE,
    name character varying(32) DEFAULT ''::character varying,
    next_check_at timestamp without time zone
);

CREATE TABLE watchers (
    id SERIAL PRIMARY KEY,
    created_at timestamp without time zone,
    last_notified_at timestamp without time zone,
    device_id integer REFERENCES devices(id) ON DELETE CASCADE,
    account_id integer REFERENCES accounts(id) ON DELETE CASCADE,
    watchee_id integer,
    upvotes integer DEFAULT 0,
    keyword character varying(32) DEFAULT ''::character varying,
    flair character varying(32) DEFAULT ''::character varying,
    domain character varying(32) DEFAULT ''::character varying,
    hits integer DEFAULT 0,
    type integer DEFAULT 0,
    label character varying(64) DEFAULT ''::character varying,
    author character varying(32) DEFAULT ''::character varying,
    subreddit character varying(32) DEFAULT ''::character varying
);

CREATE TABLE live_activities (
    id SERIAL PRIMARY KEY,
    apns_token text UNIQUE NOT NULL,
    reddit_account_id character varying(32) NOT NULL DEFAULT '',
    thread_id character varying(32) NOT NULL DEFAULT '',
    subreddit character varying(32) NOT NULL DEFAULT '',
    next_check_at timestamp without time zone,
    expires_at timestamp without time zone,
    development boolean DEFAULT FALSE
);

CREATE INDEX live_activities_next_check_at_idx ON live_activities(next_check_at);
