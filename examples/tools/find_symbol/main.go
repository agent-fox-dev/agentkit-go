// Command find_symbol calls the built-in find_symbol tool the way an agent calls it for a
// model, and prints what the model would read. See examples/tools/README.md.
//
//	go run ./examples/tools/find_symbol -schema
//	go run ./examples/tools/find_symbol '<json arguments>'
package main

import "github.com/agentfox/agentkit-go/examples/tools/toolcli"

func main() { toolcli.Main("find_symbol") }
