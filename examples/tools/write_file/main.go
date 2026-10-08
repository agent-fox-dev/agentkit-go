// Command write_file calls the built-in write_file tool the way an agent calls it for a
// model, and prints what the model would read. See examples/tools/README.md.
//
//	go run ./examples/tools/write_file -schema
//	go run ./examples/tools/write_file '<json arguments>'
package main

import "github.com/agentfox/agentkit-go/examples/tools/toolcli"

func main() { toolcli.Main("write_file") }
