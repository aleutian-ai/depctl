// Package constructorpkg is a fixture package for the godoc normalizer's
// Type.Funcs regression test (VALID-004): a package-level function
// returning a type is grouped by go/doc under that type's own Funcs
// field, not the package-level Doc.Funcs list.
package constructorpkg

// Client represents an open connection.
type Client struct{}

// NewClient constructs a Client — a package-level function that go/doc
// associates with Client (its return type), not with the package.
func NewClient() *Client {
	return &Client{}
}

// Ping checks the connection.
func (c *Client) Ping() error {
	return nil
}
