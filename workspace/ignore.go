package workspace

// IgnoreOptions injects the environment the ignore engine reads.
type IgnoreOptions struct {
	Getenv    func(string) string
	Home      func() (string, error)
	GitConfig func() string
}
