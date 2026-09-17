-- zeiten ueberall als unix-sekunden in utc

create table targets (
  id             integer primary key,
  name           text    not null,
  kind           text    not null check (kind in ('http', 'tcp', 'fivem')),
  address        text    not null,
  expect_status  text    not null default '200-399',
  keyword        text    not null default '',
  connect_to     text    not null default '',
  interval_s     integer not null default 60 check (interval_s >= 30),
  timeout_ms     integer not null default 10000 check (timeout_ms >= 500),
  fail_threshold integer not null default 3 check (fail_threshold between 1 and 20),
  public         integer not null default 0,
  paused         integer not null default 0,
  sort           integer not null default 0,
  created_at     integer not null,
  updated_at     integer not null
);

create table checks (
  id          integer primary key,
  target_id   integer not null references targets (id) on delete cascade,
  at          integer not null,
  ok          integer not null,
  latency_ms  integer not null,
  status_code integer not null default 0,
  error       text    not null default '',
  players     integer,
  max_players integer,
  version     text
);
create index checks_target_at on checks (target_id, at);

create table hourly (
  target_id integer not null references targets (id) on delete cascade,
  hour      integer not null,
  total     integer not null,
  ok_count  integer not null,
  lat_avg   integer not null,
  lat_p95   integer not null,
  lat_max   integer not null,
  primary key (target_id, hour)
);

create table incidents (
  id          integer primary key,
  target_id   integer not null references targets (id) on delete cascade,
  started_at  integer not null,
  ended_at    integer,
  cause       text    not null default '',
  mailed_down integer not null default 0,
  mailed_up   integer not null default 0
);
create index incidents_target_started on incidents (target_id, started_at);

create table admin (
  id         integer primary key check (id = 1),
  username   text    not null,
  pw_hash    text    not null,
  updated_at integer not null
);

create table sessions (
  token_hash text    primary key,
  csrf       text    not null,
  created_at integer not null,
  expires_at integer not null,
  last_seen  integer not null
);
