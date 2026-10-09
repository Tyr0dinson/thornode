package keeperv1

import "os"

func mkdirAllCov(path string) error {
	return os.MkdirAll(path, 0o755)
}

// setEnvCov sets an environment variable and returns a func restoring the
// previous value.
func setEnvCov(key, value string) func() {
	prev, had := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}
