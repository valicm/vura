-- vura schema. Everything from `sessions` down is derived and rebuildable
-- from the raw tables above it. Times are unix seconds (REAL where sub-second
-- precision arrives from the source) in UTC; the day boundary is applied at
-- query time using the configured timezone.

CREATE TABLE IF NOT EXISTS presence (
    ts            INTEGER NOT NULL,          -- unix seconds
    idle_ms       INTEGER NOT NULL,          -- ms since last input (mutter IdleMonitor)
    inhibit_flags INTEGER NOT NULL DEFAULT 0,-- gnome-session flags: 4 suspend, 8 idle
    inhibitors    TEXT,                      -- comma-joined app ids holding inhibitors
    device        TEXT
);
CREATE INDEX IF NOT EXISTS presence_ts ON presence(ts);

CREATE TABLE IF NOT EXISTS heartbeats (
    id         INTEGER PRIMARY KEY,
    ts         REAL    NOT NULL,             -- wakatime "time", float seconds
    entity     TEXT    NOT NULL,
    type       TEXT,                         -- file | app | domain | url | ai
    category   TEXT,                         -- coding | building | debugging | ai coding ...
    project    TEXT,
    branch     TEXT,
    language   TEXT,
    is_write   INTEGER NOT NULL DEFAULT 0,
    plugin     TEXT,                         -- e.g. phpstorm-wakatime/16.1.2
    editor     TEXT,                         -- e.g. phpstorm/2026.2.2
    device     TEXT,
    raw        TEXT,                         -- original JSON, for anything not modelled yet
    UNIQUE(ts, entity, plugin)
);
CREATE INDEX IF NOT EXISTS heartbeats_ts ON heartbeats(ts);
CREATE INDEX IF NOT EXISTS heartbeats_project_ts ON heartbeats(project, ts);

CREATE TABLE IF NOT EXISTS shell (
    id       TEXT PRIMARY KEY,               -- atuin history id
    ts       INTEGER NOT NULL,               -- unix seconds
    cwd      TEXT NOT NULL,
    binary   TEXT NOT NULL,                  -- first word only; arguments are never stored
    sub      TEXT,                           -- subcommand for known wrappers (git commit, ddev drush)
    duration INTEGER NOT NULL DEFAULT 0,     -- ms
    exit     INTEGER,
    session  TEXT,
    bucket   TEXT,
    device   TEXT
);
CREATE INDEX IF NOT EXISTS shell_ts ON shell(ts);

CREATE TABLE IF NOT EXISTS commits (
    sha     TEXT PRIMARY KEY,
    ts      INTEGER NOT NULL,                -- author date
    repo    TEXT NOT NULL,
    branch  TEXT,
    bucket  TEXT,
    ticket  TEXT,
    subject TEXT,
    files   INTEGER,
    ins     INTEGER,
    del     INTEGER
);
CREATE INDEX IF NOT EXISTS commits_ts ON commits(ts);

CREATE TABLE IF NOT EXISTS events (
    id        TEXT PRIMARY KEY,              -- calendar event id
    start     INTEGER NOT NULL,
    end       INTEGER NOT NULL,
    title     TEXT,
    attendees TEXT,
    bucket    TEXT,
    source    TEXT NOT NULL DEFAULT 'ics'    -- ics | slack:<workspace> (huddles)
);
CREATE INDEX IF NOT EXISTS events_start ON events(start);

CREATE TABLE IF NOT EXISTS audio (
    id        INTEGER PRIMARY KEY,
    start     INTEGER NOT NULL,              -- first seen
    last_seen INTEGER NOT NULL,              -- updated each poll while the stream is open
    end       INTEGER,                       -- set when the stream disappears; NULL = open
    app       TEXT,                          -- application.name
    binary    TEXT,                          -- application.process.binary
    media     TEXT,                          -- media.name
    source    TEXT,                          -- pipewire source node name (monitor = not a mic)
    stream    TEXT,                          -- pactl source-output index, for matching
    device    TEXT
);
CREATE INDEX IF NOT EXISTS audio_start ON audio(start);

CREATE TABLE IF NOT EXISTS notes (
    id     INTEGER PRIMARY KEY,
    ts     INTEGER NOT NULL,
    day    TEXT NOT NULL,                    -- YYYY-MM-DD working day the note is for
    bucket TEXT NOT NULL,
    hours  REAL,
    text   TEXT
);

CREATE TABLE IF NOT EXISTS anchors (
    id     TEXT PRIMARY KEY,                 -- source-qualified event id
    ts     INTEGER NOT NULL,
    source TEXT NOT NULL,                    -- github | gitlab | jira
    kind   TEXT NOT NULL,                    -- review | comment | pr | issue | push | transition | update
    ref    TEXT NOT NULL,                    -- org/repo#12, group/project!34, AUC-8600
    title  TEXT,
    url    TEXT
);
CREATE INDEX IF NOT EXISTS anchors_ts ON anchors(ts);

CREATE TABLE IF NOT EXISTS state (
    key   TEXT PRIMARY KEY,                  -- collector cursors and the like
    value TEXT
);

-- derived --------------------------------------------------------------

CREATE TABLE IF NOT EXISTS sessions (
    id      INTEGER PRIMARY KEY,
    day     TEXT NOT NULL,
    bucket  TEXT NOT NULL,
    start   INTEGER NOT NULL,
    end     INTEGER NOT NULL,
    seconds INTEGER NOT NULL,                -- observed
    source  TEXT,                            -- which signals built it, e.g. "editor:41,commit:3"
    label   TEXT,                            -- dominant project / app
    tickets TEXT,                            -- comma-joined ticket keys seen
    remote  INTEGER NOT NULL DEFAULT 0,      -- evidence while local input idle
    device  TEXT
);
CREATE INDEX IF NOT EXISTS sessions_day ON sessions(day);

CREATE TABLE IF NOT EXISTS worklogs (
    id        INTEGER PRIMARY KEY,
    day       TEXT NOT NULL,
    bucket    TEXT NOT NULL,
    issue     TEXT NOT NULL,
    start     INTEGER NOT NULL,
    seconds   INTEGER NOT NULL,              -- logged (rounded)
    observed  INTEGER NOT NULL,              -- before rounding
    desc      TEXT,
    tempo_id  TEXT,
    state     TEXT NOT NULL DEFAULT 'draft', -- draft | pushed | failed
    error     TEXT,                          -- last push error, for failed
    pushed_at INTEGER,
    UNIQUE(day, bucket, start)
);

CREATE TABLE IF NOT EXISTS days (
    day        TEXT PRIMARY KEY,
    state      TEXT NOT NULL DEFAULT 'pending', -- pending | done | skipped
    decided_at INTEGER
);

CREATE TABLE IF NOT EXISTS audit (
    id     INTEGER PRIMARY KEY,
    ts     INTEGER NOT NULL,
    action TEXT NOT NULL,
    detail TEXT
);
