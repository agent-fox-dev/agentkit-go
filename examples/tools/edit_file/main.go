// Command edit_file calls the built-in edit_file tool the way an agent calls it for a
// model, and prints what the model would read. See examples/tools/README.md.
//
//	go run ./examples/tools/edit_file -schema
//	go run ./examples/tools/edit_file '<json arguments>'
package main

import "github.com/agentfox/agentkit-go/examples/tools/toolcli"

func main() { toolcli.Main("edit_file") }
