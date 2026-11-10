package telemetry

const (
	OutcomeSuccess = "success"
	OutcomeError   = "error"
	OutcomePanic   = "panic"

	ModeExecution  = "execution"
	ModeSimulation = "simulation"

	ContractInstantiate  = "instantiate"
	ContractInstantiate2 = "instantiate2"
	ContractExecute      = "execute"
	ContractMigrate      = "migrate"
	ContractSudo         = "sudo"
)

// Outcome returns the execution telemetry result label for an error.
func Outcome(err error) string {
	if err != nil {
		return OutcomeError
	}
	return OutcomeSuccess
}
