-- 'cloud' is sent as tickets.source by the Cloud Posture "Create Ticket" action
-- (app CloudPostureView.tsx) but was never added to ticket_source_type, so every
-- insert into tickets from that page violates tickets_source_fkey after the
-- ticket has already been created in the external tool (Jira/ServiceNow/etc).
-- See GitHub issue #39098.
INSERT INTO "public"."ticket_source_type"("value") VALUES ('cloud')
ON CONFLICT DO NOTHING;
