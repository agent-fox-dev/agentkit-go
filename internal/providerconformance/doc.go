// Package providerconformance holds the opt-in live tests of the anthropic,
// openrouter and ollama providers.
//
// TestLive runs one real tool loop on each provider whose prerequisites are
// present, and only when asked: make provider-live-test. Each provider's wire
// is tested offline, against a fake of its vendor's API, in its own package.
//
// Every line of code is in a _test.go file. No package imports this one.
package providerconformance
