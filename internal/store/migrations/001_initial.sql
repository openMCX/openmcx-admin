-- The MCX Admin store (docs/SLICE_B.md): revisions of the configuration document,
-- the audit trail and the outbox of changes for the consumers of slice C.
CREATE TABLE revisions (
  id          BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  created_at  DATETIME(3)  NOT NULL,
  actor       VARCHAR(255) NOT NULL,
  reason      VARCHAR(1024) NOT NULL,
  source      VARCHAR(32)  NOT NULL,
  document    LONGTEXT     NOT NULL,
  changes     LONGTEXT     NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE audit (
  id          BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  created_at  DATETIME(3)  NOT NULL,
  actor       VARCHAR(255) NOT NULL,
  action      VARCHAR(64)  NOT NULL,
  target      VARCHAR(512) NOT NULL,
  revision_id BIGINT       NULL,
  detail      TEXT         NOT NULL,
  INDEX (revision_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE outbox (
  id          BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
  revision_id BIGINT       NOT NULL,
  consumer    VARCHAR(32)  NOT NULL,
  status      VARCHAR(16)  NOT NULL,
  attempts    INT          NOT NULL DEFAULT 0,
  last_error  TEXT         NULL,
  updated_at  DATETIME(3)  NOT NULL,
  UNIQUE KEY (revision_id, consumer),
  INDEX (consumer, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
