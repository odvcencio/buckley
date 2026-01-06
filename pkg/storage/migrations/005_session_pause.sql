-- Migration: Add pause state columns to sessions table
-- This enables durable persistence of workflow pause state across restarts

ALTER TABLE sessions ADD COLUMN pause_reason TEXT;
ALTER TABLE sessions ADD COLUMN pause_question TEXT;
ALTER TABLE sessions ADD COLUMN paused_at TIMESTAMP;
