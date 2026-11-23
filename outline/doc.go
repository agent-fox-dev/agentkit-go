// Package outline returns the declarations of a source file, with line
// ranges, and the parse verdict of the parse that produced them: whether
// the file was parsed, why not if it was not, and the syntax errors the
// parse found. Go is parsed with go/parser in every build; fourteen other
// languages with their tree-sitter grammars in a build with cgo. One parse
// gives both the declarations and the verdict, so a file is never outlined
// by one backend and checked by another.
//
// A syntax error is a position and a kind (unexpected text, or a token
// that is missing where it was expected), never a message. Semantic errors
// (unresolved names, types, unused imports) are not reported: only what the
// parser rejects.
//
// A grammar can lag its language: a file that uses syntax newer than the
// grammar of this build is reported with syntax errors although the
// language accepts it. A caller that refuses files, or edits, on this
// verdict should offer a way past it.
package outline
