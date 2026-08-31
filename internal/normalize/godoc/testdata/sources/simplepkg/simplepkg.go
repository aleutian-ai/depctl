// Package simplepkg is a fixture package for godoc normalizer tests.
package simplepkg

// DefaultTimeout is the default timeout in seconds.
const DefaultTimeout = 30

const internalFlag = true

// Greet returns a friendly greeting for name.
func Greet(name string) string {
	return "Hello, " + name
}

func helper() string {
	return "unexported"
}

// Config holds simplepkg's configuration.
type Config struct {
	Name string
}

// Describe returns a human-readable summary of c.
func (c Config) Describe() string {
	return "Config: " + c.Name
}
