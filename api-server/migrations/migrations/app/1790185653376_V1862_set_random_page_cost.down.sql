DO $$
BEGIN
  EXECUTE format('ALTER DATABASE %I RESET random_page_cost', current_database());
END $$;
