-- Superseded rollouts are recorded by the OutcomeLogger when a newer deployment replaces them.
ALTER TABLE deployment_history DROP CONSTRAINT deployment_history_outcome_check,
  ADD CONSTRAINT deployment_history_outcome_check CHECK (outcome IN ('completed','failed','superseded'));
