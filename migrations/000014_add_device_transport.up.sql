ALTER TABLE devices
    ADD COLUMN transport character varying(16) NOT NULL DEFAULT 'apns',
    ADD COLUMN transport_endpoint text NOT NULL DEFAULT '';
