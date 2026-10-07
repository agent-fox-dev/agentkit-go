package codesearch

// BuildStatsResult holds statistics from a build. It is declared without a
// build constraint: the Windows stub returns the zero value.
type BuildStatsResult struct {
	BinarySkipped    int
	OversizedSkipped int
	// TooManyTrigramsSkipped counts files zoekt would store as "not
	// indexed" for having more distinct trigrams than it indexes per file
	// (minified bundles, lock files, source maps).
	TooManyTrigramsSkipped int
	// TooSmallSkipped counts files under three bytes, which hold no trigram.
	TooSmallSkipped     int
	CtagsProcessSpawned bool
	FilesIndexed        int
}
