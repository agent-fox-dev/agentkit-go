// Package providerconformance runs the same logical turns through the
// anthropic, openrouter and ollama providers and requires one canonical
// outcome, and holds the opt-in live smoke tests of the three.
//
// TestProviderConformance builds each agent with agentkit.New from a provider
// name and points it at a local fake of that vendor's native API, so it runs
// offline on every change. TestLive runs one real tool loop on each provider
// whose prerequisites are present, and only when asked: make
// provider-live-test.
//
// Every line of code is in a _test.go file, the httptest servers included, so
// no non-test file of the module listens on a socket. No package imports this
// one.
package providerconformance
