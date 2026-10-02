package codesearch

import "errors"

// ErrUnsupported is returned by New on platforms where zoekt cannot build or
// run (currently windows/amd64). An embedder should fall back to
// Options.Index == nil when errors.Is(err, ErrUnsupported) is true.
var ErrUnsupported = errors.New("codesearch: unsupported platform")
