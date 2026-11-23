-- SPDX-License-Identifier: AGPL-3.0-or-later

ALTER TABLE link_samples ADD COLUMN target_id TEXT NOT NULL DEFAULT '';
