// Command file_outline calls the built-in file_outline tool the way an agent calls it for a
// model, and prints what the model would read. See examples/tools/README.md.
//
//	go run ./examples/tools/file_outline -schema
//	go run ./examples/tools/file_outline '<json arguments>'
package main

import "github.com/agentfox/agentkit-go/examples/tools/toolcli"

func main() { toolcli.Main("file_outline") }
