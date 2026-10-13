// Package confine is the only package that builds and applies a Landlock
// ruleset. Only the agentkit-sandboxd command imports it, so nothing the
// local provider runs inside an embedder's process can reach it. It applies a
// configuration of exactly the ABI it chose and never calls BestEffort, which
// succeeds silently on a kernel without Landlock.
package confine
