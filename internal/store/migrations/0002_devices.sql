-- bekannte browser, nur der hash des cookies steht hier

create table devices (
  token_hash text    primary key,
  created_at integer not null,
  expires_at integer not null
);
