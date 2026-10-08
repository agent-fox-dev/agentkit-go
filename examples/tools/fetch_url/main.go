// Command fetch_url calls the built-in fetch_url tool the way an agent calls it for a
// model, and prints what the model would read. See examples/tools/README.md.
//
//	go run ./examples/tools/fetch_url -schema
//	go run ./examples/tools/fetch_url '<json arguments>'
package main

import "github.com/agentfox/agentkit-go/examples/tools/toolcli"

func main() { toolcli.Main("fetch_url") }
