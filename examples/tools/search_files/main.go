// Command search_files calls the built-in search_files tool the way an agent calls it for a
// model, and prints what the model would read. See examples/tools/README.md.
//
//	go run ./examples/tools/search_files -schema
//	go run ./examples/tools/search_files '<json arguments>'
package main

import "github.com/agentfox/agentkit-go/examples/tools/toolcli"

func main() { toolcli.Main("search_files") }
