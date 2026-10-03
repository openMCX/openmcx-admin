-- The GMS version of each group after MCX Admin's own write (slice D3): a higher
-- version at the GMS is a change made there, by a user's XCAP write.
CREATE TABLE gms_versions (
  group_id VARCHAR(255) PRIMARY KEY,
  version BIGINT NOT NULL
);
