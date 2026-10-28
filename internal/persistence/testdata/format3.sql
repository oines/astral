-- Frozen public Host format 3. This fixture must not follow current schema or
-- migration helpers: it represents a database written before activation cursors.
CREATE TABLE documents(kind TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id));
CREATE TABLE versions(kind TEXT NOT NULL,id TEXT NOT NULL,revision INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id,revision));
CREATE TABLE secrets(id TEXT PRIMARY KEY,value BLOB NOT NULL);
CREATE TABLE observations(sequence INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,connection_id TEXT NOT NULL,conversation_id TEXT NOT NULL,external_id TEXT NOT NULL,payload BLOB NOT NULL,UNIQUE(connection_id,external_id));
CREATE INDEX observation_conversation ON observations(conversation_id,sequence);
CREATE TABLE deliveries(event_id TEXT PRIMARY KEY,session_id TEXT NOT NULL,state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '');
CREATE TABLE operations(id TEXT PRIMARY KEY,kind TEXT NOT NULL,state TEXT NOT NULL,data BLOB NOT NULL);
CREATE TABLE outbox_keys(intent_key TEXT PRIMARY KEY,outbox_id TEXT NOT NULL);
CREATE TABLE goose_db_version(id INTEGER PRIMARY KEY AUTOINCREMENT,version_id INTEGER NOT NULL,is_applied INTEGER NOT NULL,tstamp TIMESTAMP DEFAULT (datetime('now')));
INSERT INTO goose_db_version(version_id,is_applied) VALUES(0,1),(3,1);
PRAGMA user_version=3;
