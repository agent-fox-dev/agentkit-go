package sandbox

// LandlockMinABI is the lowest Landlock ABI an agent applies: under ABI 1
// every cross-directory rename or link is denied, which breaks git and other
// ordinary tools, so a kernel offering only ABI 1 gets no ruleset.
const LandlockMinABI = 2

// LandlockTCPABI is the Landlock ABI that restricts TCP bind and connect,
// and the highest an agent applies.
const LandlockTCPABI = 4

// LandlockInfo is what the agent's Landlock confinement applied to its own
// process, and so to every process a tool starts.
type LandlockInfo struct {
	// ABI is the ABI of the ruleset applied; 0 means none.
	ABI int
	// KernelABI is the ABI the kernel offered; 0 means none or unknown.
	KernelABI int
	// TCP reports that TCP bind and connect are denied.
	TCP bool
	// Note says why nothing, or only part, was applied.
	Note string
	// Writable lists the resolved roots the ruleset grants every right
	// beneath, then the device files it grants read and write (and, from
	// ABI 3, truncate), in the order their rules were added.
	Writable []string
	// ReadOnly lists the resolved roots the ruleset grants read (and, for
	// all but /proc, execute) beneath, in the order their rules were added.
	ReadOnly []string
}

// Satisfies reports whether the ruleset is enough for a Spec with
// RequireLandlock under network mode mode: an ABI of at least
// LandlockMinABI and, under NetworkNone, TCP denied.
func (l LandlockInfo) Satisfies(mode NetworkMode) bool {
	return l.ABI >= LandlockMinABI && (mode != NetworkNone || l.TCP)
}
