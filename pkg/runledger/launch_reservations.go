package runledger

import (
	"context"
	"errors"
	"fmt"

	"m31labs.dev/buckley/pkg/storage"
)

func createLaunchReservationTables(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS launch_budget_accounts (
			run_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			profile_version TEXT NOT NULL,
			profile_digest TEXT NOT NULL,
			envelope_digest TEXT NOT NULL,
			model_requests_limit INTEGER NOT NULL,
			input_tokens_limit INTEGER NOT NULL,
			output_tokens_limit INTEGER NOT NULL,
			total_tokens_limit INTEGER NOT NULL,
			max_output_per_request INTEGER NOT NULL,
			request_timeout_ms INTEGER NOT NULL,
			turn_timeout_ms INTEGER NOT NULL,
			absolute_run_timeout_ms INTEGER NOT NULL,
			global_capacity INTEGER NOT NULL,
			per_run_parallelism INTEGER NOT NULL,
			provider_post_attempts INTEGER NOT NULL,
			manager_affordability_attempts INTEGER NOT NULL,
			retry_owner TEXT NOT NULL,
			started_at TIMESTAMP NOT NULL,
			run_deadline_at TIMESTAMP NOT NULL,
			CHECK (model_requests_limit > 0),
			CHECK (input_tokens_limit > 0),
			CHECK (output_tokens_limit > 0),
			CHECK (total_tokens_limit > 0),
			CHECK (max_output_per_request > 0),
			CHECK (request_timeout_ms > 0),
			CHECK (turn_timeout_ms > 0),
			CHECK (absolute_run_timeout_ms > 0),
			CHECK (global_capacity > 0),
			CHECK (per_run_parallelism > 0),
			CHECK (provider_post_attempts = 1),
			CHECK (manager_affordability_attempts = 1),
			CHECK (retry_owner = 'dapr'),
			UNIQUE (run_id, session_id),
			FOREIGN KEY(run_id, session_id)
				REFERENCES launch_envelopes(run_id, session_id) ON DELETE CASCADE
		);
		CREATE TABLE IF NOT EXISTS launch_turn_windows (
			run_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			profile_digest TEXT NOT NULL,
			envelope_digest TEXT NOT NULL,
			started_at TIMESTAMP NOT NULL,
			deadline_at TIMESTAMP NOT NULL,
			PRIMARY KEY(run_id, turn_id),
			UNIQUE(run_id, session_id, turn_id),
			FOREIGN KEY(run_id, session_id)
				REFERENCES launch_budget_accounts(run_id, session_id) ON DELETE CASCADE
		);
		CREATE TABLE IF NOT EXISTS launch_model_reservations (
			run_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			step_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			input_digest TEXT NOT NULL,
			wire_request_digest TEXT NOT NULL,
			profile_digest TEXT NOT NULL,
			envelope_digest TEXT NOT NULL,
			step_attempt INTEGER NOT NULL,
			step_claim_generation INTEGER NOT NULL,
			lease_generation INTEGER NOT NULL,
			state TEXT NOT NULL,
			reserved_input_tokens INTEGER NOT NULL,
			reserved_output_tokens INTEGER NOT NULL,
			reserved_total_tokens INTEGER NOT NULL,
			actual_input_tokens INTEGER,
			actual_output_tokens INTEGER,
			actual_total_tokens INTEGER,
			reserved_at TIMESTAMP NOT NULL,
			lease_expires_at TIMESTAMP NOT NULL,
			request_deadline_at TIMESTAMP,
			dispatched_at TIMESTAMP,
			request_evidence_id TEXT,
			request_evidence_digest TEXT,
			response_evidence_id TEXT,
			output_digest TEXT,
			terminal_reason_code TEXT,
			terminal_at TIMESTAMP,
			PRIMARY KEY(run_id, step_id, step_attempt),
			CHECK (step_attempt > 0),
			CHECK (step_claim_generation > 0),
			CHECK (lease_generation > 0),
			CHECK (state IN ('reserved','dispatched','settled','released','ambiguous','breached')),
			CHECK (reserved_input_tokens >= 0),
			CHECK (reserved_output_tokens > 0),
			CHECK (reserved_total_tokens > 0),
			CHECK ((actual_input_tokens IS NULL) = (actual_output_tokens IS NULL)),
			CHECK ((actual_input_tokens IS NULL) = (actual_total_tokens IS NULL)),
			UNIQUE(run_id, session_id, step_id, step_attempt),
			FOREIGN KEY(run_id, session_id)
				REFERENCES launch_budget_accounts(run_id, session_id) ON DELETE CASCADE,
			FOREIGN KEY(run_id, session_id, turn_id)
				REFERENCES launch_turn_windows(run_id, session_id, turn_id) ON DELETE CASCADE,
			FOREIGN KEY(run_id, step_id)
				REFERENCES execution_steps(run_id, step_id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_launch_model_reservations_active_step
			ON launch_model_reservations(run_id, step_id)
			WHERE state IN ('reserved','dispatched');
		CREATE INDEX IF NOT EXISTS idx_launch_model_reservations_active_global
			ON launch_model_reservations(state, lease_expires_at, run_id, step_id)
			WHERE state IN ('reserved','dispatched');
		CREATE INDEX IF NOT EXISTS idx_launch_model_reservations_run_budget
			ON launch_model_reservations(run_id, state, step_attempt);
		CREATE TRIGGER IF NOT EXISTS trg_launch_budget_accounts_immutable
		BEFORE UPDATE ON launch_budget_accounts
		BEGIN
			SELECT RAISE(ABORT, 'launch budget account is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_agent_runs_coupled_replace
		BEFORE INSERT ON agent_runs
		WHEN EXISTS (SELECT 1 FROM agent_runs WHERE run_id = NEW.run_id)
		 AND EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch run is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_agent_runs_coupled_update_replace
		BEFORE UPDATE OF run_id, session_id ON agent_runs
		WHEN (OLD.run_id IS NOT NEW.run_id OR OLD.session_id IS NOT NEW.session_id)
		 AND (
			EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = OLD.run_id) OR
			EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		 )
		BEGIN
			SELECT RAISE(ABORT, 'launch run identity is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_run_contracts_coupled_replace
		BEFORE INSERT ON agent_run_contracts
		WHEN EXISTS (SELECT 1 FROM agent_run_contracts WHERE run_id = NEW.run_id)
		 AND EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch run contract is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_run_contracts_coupled_update_replace
		BEFORE UPDATE OF run_id, session_id ON agent_run_contracts
		WHEN (OLD.run_id IS NOT NEW.run_id OR OLD.session_id IS NOT NEW.session_id)
		 AND (
			EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = OLD.run_id) OR
			EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		 )
		BEGIN
			SELECT RAISE(ABORT, 'launch run contract identity is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_envelopes_duplicate_immutable
		BEFORE INSERT ON launch_envelopes
		WHEN EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch envelope is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_budget_accounts_duplicate_immutable
		BEFORE INSERT ON launch_budget_accounts
		WHEN EXISTS (SELECT 1 FROM launch_budget_accounts WHERE run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch budget account is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_budget_accounts_delete_immutable
		BEFORE DELETE ON launch_budget_accounts
		WHEN EXISTS (
			SELECT 1 FROM launch_envelopes
			WHERE run_id = OLD.run_id AND session_id = OLD.session_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch budget account is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_turn_windows_immutable
		BEFORE UPDATE ON launch_turn_windows
		BEGIN
			SELECT RAISE(ABORT, 'launch turn window is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_turn_windows_duplicate_immutable
		BEFORE INSERT ON launch_turn_windows
		WHEN EXISTS (
			SELECT 1 FROM launch_turn_windows
			WHERE run_id = NEW.run_id AND turn_id = NEW.turn_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch turn window is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_turn_windows_delete_immutable
		BEFORE DELETE ON launch_turn_windows
		WHEN EXISTS (
			SELECT 1 FROM launch_budget_accounts
			WHERE run_id = OLD.run_id AND session_id = OLD.session_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch turn window is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_identity_immutable
		BEFORE UPDATE OF run_id, session_id, task_id, turn_id, step_id, kind,
			idempotency_key, input_digest, wire_request_digest, profile_digest, envelope_digest,
			step_attempt, step_claim_generation, lease_generation,
			reserved_input_tokens, reserved_output_tokens,
			reserved_total_tokens, reserved_at
		ON launch_model_reservations
		BEGIN
			SELECT RAISE(ABORT, 'launch model reservation identity is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_duplicate_immutable
		BEFORE INSERT ON launch_model_reservations
		WHEN EXISTS (
			SELECT 1 FROM launch_model_reservations
			WHERE run_id = NEW.run_id AND step_id = NEW.step_id
			  AND step_attempt = NEW.step_attempt
		) OR (
			NEW.state IN ('reserved','dispatched') AND EXISTS (
				SELECT 1 FROM launch_model_reservations
				WHERE run_id = NEW.run_id AND step_id = NEW.step_id
				  AND state IN ('reserved','dispatched')
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch model reservation is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_transition
		BEFORE UPDATE OF state ON launch_model_reservations
		WHEN NOT (
			(OLD.state = 'reserved' AND NEW.state IN ('dispatched','released')) OR
			(OLD.state = 'dispatched' AND NEW.state IN ('settled','ambiguous','breached'))
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid launch model reservation transition');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_same_state_immutable
		BEFORE UPDATE ON launch_model_reservations
		WHEN OLD.state = NEW.state
		BEGIN
			SELECT RAISE(ABORT, 'launch model reservation state is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_delete_immutable
		BEFORE DELETE ON launch_model_reservations
		WHEN EXISTS (
			SELECT 1 FROM launch_budget_accounts
			WHERE run_id = OLD.run_id AND session_id = OLD.session_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch model reservation is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_execution_steps_coupled_update
		BEFORE UPDATE ON execution_steps
		WHEN EXISTS (
			SELECT 1 FROM launch_model_reservations
			WHERE run_id = OLD.run_id AND step_id = OLD.step_id
		) AND NOT EXISTS (
			SELECT 1 FROM launch_model_reservations AS reservation
			WHERE reservation.run_id = NEW.run_id AND reservation.step_id = NEW.step_id
			  AND reservation.task_id IS NEW.task_id
			  AND reservation.kind = NEW.kind
			  AND reservation.idempotency_key = NEW.idempotency_key
			  AND reservation.input_digest IS NEW.input_digest
			  AND reservation.step_attempt = NEW.attempt
			  AND reservation.step_claim_generation = NEW.claim_generation
			  AND reservation.reserved_at = NEW.started_at
			  AND (
				(reservation.state = 'reserved' AND NEW.status = 'started'
				 AND NEW.dispatch_state = 'claimed' AND NEW.completed_at IS NULL
				 AND NEW.output_evidence_id IS NULL AND NEW.output_digest IS NULL AND NEW.error_text IS NULL) OR
				(reservation.state = 'dispatched' AND NEW.status = 'started'
				 AND NEW.dispatch_state = 'dispatched' AND NEW.completed_at IS NULL
				 AND NEW.output_evidence_id IS NULL AND NEW.output_digest IS NULL AND NEW.error_text IS NULL) OR
				(reservation.state IN ('settled','breached') AND NEW.status = 'completed'
				 AND NEW.dispatch_state = 'dispatched' AND NEW.completed_at IS reservation.terminal_at
				 AND NEW.output_evidence_id IS reservation.response_evidence_id
				 AND NEW.output_digest IS reservation.output_digest AND NEW.error_text IS NULL) OR
				(reservation.state = 'ambiguous' AND NEW.status = 'blocked'
				 AND NEW.dispatch_state = 'dispatched' AND NEW.completed_at IS reservation.terminal_at
				 AND NEW.output_evidence_id IS reservation.response_evidence_id
				 AND NEW.output_digest IS reservation.output_digest
				 AND NEW.error_text IS reservation.terminal_reason_code) OR
				(reservation.state = 'released' AND NEW.status = 'failed'
				 AND NEW.dispatch_state = 'claimed' AND NEW.completed_at IS reservation.terminal_at
				 AND NEW.output_evidence_id IS NULL AND NEW.output_digest IS NULL
				 AND NEW.error_text IS reservation.terminal_reason_code)
			  )
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch execution step mutation is not paired');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_execution_steps_coupled_update_replace
		BEFORE UPDATE OF run_id, step_id, idempotency_key ON execution_steps
		WHEN EXISTS (
			SELECT 1
			FROM launch_model_reservations AS reservation
			JOIN execution_steps AS existing
			  ON existing.run_id = reservation.run_id AND existing.step_id = reservation.step_id
			WHERE existing.run_id = NEW.run_id
			  AND (existing.step_id = NEW.step_id OR existing.idempotency_key = NEW.idempotency_key)
			  AND NOT (existing.run_id = OLD.run_id AND existing.step_id = OLD.step_id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch execution step replacement target is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_execution_steps_coupled_delete
		BEFORE DELETE ON execution_steps
		WHEN EXISTS (
			SELECT 1 FROM launch_model_reservations
			WHERE run_id = OLD.run_id AND step_id = OLD.step_id
		) AND EXISTS (SELECT 1 FROM agent_runs WHERE run_id = OLD.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch execution step is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_execution_steps_coupled_replace
		BEFORE INSERT ON execution_steps
		WHEN EXISTS (
			SELECT 1
			FROM launch_model_reservations AS reservation
			JOIN execution_steps AS existing
			  ON existing.run_id = reservation.run_id AND existing.step_id = reservation.step_id
			WHERE existing.run_id = NEW.run_id
			  AND (existing.step_id = NEW.step_id OR existing.idempotency_key = NEW.idempotency_key)
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch execution step is coupled');
		END;
	`)
	if err != nil {
		return fmt.Errorf("create launch reservation tables: %w", err)
	}
	return nil
}

func (s *SQLiteStore) guardGenericLaunchStepMutation(ctx context.Context, runID, stepID string) error {
	if s == nil || s.db == nil {
		return errors.New("runledger: execution step journal is unavailable")
	}
	var coupled int
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM launch_model_reservations
			WHERE run_id = ? AND step_id = ?
		)
	`, runID, stepID).Scan(&coupled); err != nil {
		return fmt.Errorf("runledger: inspect launch-coupled execution step: %w", err)
	}
	if coupled != 0 {
		return fmt.Errorf("%w: execution step %s is launch-reservation coupled", ErrStepTransitionConflict, stepID)
	}
	return nil
}
