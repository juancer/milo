-- Remote administration through Telegram. Both switches are opt-in and off by default:
--   forward_incoming: send this person a copy of messages other people leave for the device
--   device_alerts:    tell this person when a feature fails or the device starts
ALTER TABLE users ADD COLUMN forward_incoming INTEGER NOT NULL DEFAULT 0 CHECK (forward_incoming IN (0, 1));
ALTER TABLE users ADD COLUMN device_alerts    INTEGER NOT NULL DEFAULT 0 CHECK (device_alerts IN (0, 1));
