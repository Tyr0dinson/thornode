package endpoints

// State is the health state of one configured chain endpoint.
type State int8

const (
	// StateHealthy serves traffic.
	StateHealthy State = iota
	// StateUnhealthy exceeded the error threshold (or lags, or failed probes) and is
	// skipped by readers while any healthy endpoint exists.
	StateUnhealthy
	// StateRecovering passed at least one probe after being unhealthy but has not yet
	// reached min_recovery_checks. It does not serve traffic.
	StateRecovering
	// StateDisabled reported a chain identity different from the reference. It never
	// serves traffic again until bifrost restarts.
	StateDisabled
)

func (s State) String() string {
	switch s {
	case StateHealthy:
		return "healthy"
	case StateUnhealthy:
		return "unhealthy"
	case StateRecovering:
		return "recovering"
	case StateDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// ErrorKind labels why an error was recorded against an endpoint. It is the "kind"
// label of bifrost_endpoint_errors_total.
type ErrorKind string

const (
	ErrorKindTransport ErrorKind = "transport" // dial, DNS, TLS, timeout
	ErrorKindHTTP      ErrorKind = "http"      // 3xx, 5xx, 401, 403, JSON-RPC server error, gRPC unavailable/internal/unauthenticated
	ErrorKindProbe     ErrorKind = "probe"     // background health probe failed
	ErrorKindLag       ErrorKind = "lag"       // behind consensus tip or behind the scanner
	ErrorKindIntegrity ErrorKind = "integrity" // block hash disagrees with the majority
	ErrorKindRateLimit ErrorKind = "rate_limit"
)
