// Package output is the bounded output buffer the tools and code mode share:
// an Accumulator keeps the head, the tail or both ends of a long stream within
// a byte budget, and may spill the whole of it to a file.
//
// It is a package of its own, importing only the standard library, so that a
// program can bound a script's output without linking the source tools and the
// tree-sitter grammars that come with them. Package tools keeps its own names
// for these types as aliases.
package output
