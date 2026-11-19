package main

// sample is the small Go project the example serves: three files that
// mention needle, so a search finds them, and one an edit will change.
var sample = map[string]string{
	"go.mod":    "module sample\n\ngo 1.27\n",
	"a.go":      "package sample\n\n// Needle is what the first search finds: needle.\nfunc Needle() int { return 1 }\n",
	"b.go":      "package sample\n\n// Other is the file the edit changes; it mentions needle.\nfunc Other() int { return 2 }\n",
	"c.go":      "package sample\n\n// Third mentions needle too.\nfunc Third() int { return 3 }\n",
	"README.md": "A sample project for the toolserver example.\n",
}
