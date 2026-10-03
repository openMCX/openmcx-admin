-- One-time activation codes (slice F1): only a code's SHA-256 is stored.
CREATE TABLE activations (
  code_hash   CHAR(64)     NOT NULL PRIMARY KEY,
  login       VARCHAR(255) NOT NULL,
  created_by  VARCHAR(255) NOT NULL,
  created_at  DATETIME(3)  NOT NULL,
  expires_at  DATETIME(3)  NOT NULL,
  used_at     DATETIME(3)  NULL,
  INDEX (login)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
