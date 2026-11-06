ALTER TABLE global_admins
    ADD COLUMN label TEXT NOT NULL DEFAULT '' CHECK (char_length(label) <= 100);
