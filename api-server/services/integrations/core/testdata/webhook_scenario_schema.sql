create extension if not exists pgcrypto;
create table tenant (id uuid primary key);
create table integration_types (name text primary key, category text);
create table integrations (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references tenant(id),
  type text not null references integration_types(name),
  source text, name text, status text,
  created_at timestamp default now(), updated_at timestamp default now(),
  created_by uuid, updated_by uuid, labels jsonb default '{}'
);
create table integrations_cloud_accounts (integration_id uuid, cloud_account_id uuid);
create table integration_config_values (integration_id uuid, name text, value text);
create table event_incoming_webhooks (
  id uuid primary key default gen_random_uuid(),
  tenant_id uuid not null references tenant(id),
  account_id uuid,
  integration_id uuid not null references integrations(id),
  integration_type text not null references integration_types(name),
  webhook_id text not null, event_type text not null, event_id text not null,
  event_url text, event_status text, event_priority text,
  event_created_at timestamp not null, event_title text not null,
  event_description text, raw text,
  processing_status text not null default 'processed', request_url text,
  processing_error text
);
insert into integration_types(name, category) values ('workflow_webhook','incident_webhook');
insert into tenant(id) values ('890cad87-c452-4aa7-b84a-742cee0454a1');
insert into integrations(id, tenant_id, type, source, name, status)
  values ('0e75a0cc-a55c-4187-afc1-8acf5aead026','890cad87-c452-4aa7-b84a-742cee0454a1','workflow_webhook','user','wf-fb836ea7-github_ci_webhook','enabled');
insert into integration_config_values(integration_id, name, value)
  values ('0e75a0cc-a55c-4187-afc1-8acf5aead026','token','scenario-token-123');
alter table integration_config_values add column if not exists is_encrypted boolean default false;
create table if not exists cloud_accounts (id uuid primary key default gen_random_uuid(), tenant uuid);
insert into cloud_accounts(id, tenant) values ('a2a30b02-0f67-42e5-a2ab-c658230fd798','890cad87-c452-4aa7-b84a-742cee0454a1') on conflict do nothing;
