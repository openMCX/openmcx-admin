-- One row every configuration write locks first, so writers are serialised and
-- each then reads the latest revision (a locking read that waited on a revision
-- row would otherwise return that row, no longer the latest).
CREATE TABLE write_lock (
  id TINYINT PRIMARY KEY
);
INSERT INTO write_lock (id) VALUES (1);
