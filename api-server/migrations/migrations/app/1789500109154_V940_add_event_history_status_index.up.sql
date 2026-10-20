CREATE INDEX IF NOT EXISTS idx_event_history_event_status
    ON public.event_history (event_id, changed_at DESC)
    WHERE change_type = 'status';
