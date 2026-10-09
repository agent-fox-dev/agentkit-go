package codemode

// BuildInfo reports what a code-mode tool costs in the model's context
// before it is sent: the size of its generated description, which grows with
// every tool bound to it.
type BuildInfo struct {
	// DescriptionBytes is the description's length in bytes.
	DescriptionBytes int
	// DescriptionChars is its length in Unicode characters.
	DescriptionChars int
	// BoundToolsCount is the number of tools bound to the tool.
	BoundToolsCount int
}
