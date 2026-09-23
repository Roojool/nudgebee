DO $$
BEGIN
  EXECUTE format('ALTER DATABASE %I SET random_page_cost = 1.5', current_database());
END $$;
