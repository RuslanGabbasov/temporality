DROP INDEX IF EXISTS learning_program_user_unique;
CREATE INDEX IF NOT EXISTS learning_program_user_idx ON public.learning_program (user_id, status);
