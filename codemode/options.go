package codemode

import (
	"os"
	"path/filepath"
	"time"
)

// Options configures a code-mode tool. The zero value is valid: every zero
// field takes the default DefaultOptions documents.
type Options struct {
	// Name is the tool's name. Default "code_mode".
	Name string
	// Description, when set, is the tool's description verbatim, and nothing
	// is generated.
	Description string
	// DescriptionTemplate, when set, is a text/template that replaces the
	// default instructions section of the generated description.
	DescriptionTemplate string
	// Guidelines become the tool's PromptGuidelines. Default: the built-in
	// code-mode guidance.
	Guidelines []string

	// MaxTimeout bounds a script's wall time. Default 30s.
	MaxTimeout time.Duration
	// MaxSteps bounds a script's Starlark execution steps. Default 100,000.
	MaxSteps uint64
	// MaxCalls bounds the nested tool calls one script makes. Default 50.
	MaxCalls int
	// MaxConcurrentCalls bounds the calls one parallel(...) has in flight at
	// once. Default 8.
	MaxConcurrentCalls int
	// MaxOutputBytes bounds what a script prints and returns. Default
	// 102,400 (100 KB).
	MaxOutputBytes int
	// SpillDir receives the complete output of a script whose output was
	// truncated. Default: agentkit-codemode under the OS temp directory.
	SpillDir string
	// DisableSpill turns spill files off.
	DisableSpill bool
}

// Defaults for Options' zero fields.
const (
	DefaultName                      = "code_mode"
	DefaultMaxTimeout                = 30 * time.Second
	DefaultMaxSteps           uint64 = 100_000
	DefaultMaxCalls                  = 50
	DefaultMaxConcurrentCalls        = 8
	DefaultMaxOutputBytes            = 100 * 1024
)

// DefaultOptions returns Options with every default filled in.
func DefaultOptions() Options { return Options{}.withDefaults() }

func (o Options) withDefaults() Options {
	if o.Name == "" {
		o.Name = DefaultName
	}
	if o.MaxTimeout <= 0 {
		o.MaxTimeout = DefaultMaxTimeout
	}
	if o.MaxSteps == 0 {
		o.MaxSteps = DefaultMaxSteps
	}
	if o.MaxCalls <= 0 {
		o.MaxCalls = DefaultMaxCalls
	}
	if o.MaxConcurrentCalls <= 0 {
		o.MaxConcurrentCalls = DefaultMaxConcurrentCalls
	}
	if o.MaxOutputBytes <= 0 {
		o.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if o.SpillDir == "" {
		o.SpillDir = filepath.Join(os.TempDir(), "agentkit-codemode")
	}
	return o
}
