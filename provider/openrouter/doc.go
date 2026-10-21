// Package openrouter is a core.ProviderClient for a model served by OpenRouter.
// It reaches OpenRouter through OpenRouter's own Go SDK and its native Chat
// Completions API, and owns what the SDK does not: turning a canonical request
// into an OpenRouter request, streaming the response back as canonical events
// and an assistant message, and handling credentials, errors, retries,
// reasoning replay, usage and reported cost the way OpenRouter documents them.
//
// The provider reads no model metadata of its own: the window, the output cap,
// the prices and the efforts a model takes come from the core.Model passed to
// New, which package catalog supplies.
package openrouter
