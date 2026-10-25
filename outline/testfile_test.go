package outline

import "testing"

func TestIsTestFileFollowsTheLanguagesConventions(t *testing.T) {
	for want, paths := range map[bool][]string{
		true: {
			"a_test.go", "pkg/a_test.go", "test_x.py", "x_test.py", "src/lib_test.rs", "test_util.c", "foo_test.rb",
			"a.test.ts", "a.spec.js", "a.test.tsx", "a.spec.mjs", "foo_spec.rb",
			"FooTest.java", "FooTests.kt", "FooTest.cs", "FooTests.rb", "OrderServiceIT.java",
			"conftest.py", "app/conftest.py", "setupTests.ts", "jest.setup.js", "vitest.setup.ts",
			"tests/x.go", "__tests__/a.js", "spec/a.rb", "test/a.c", "pkg/test/deep.go",
			"testdata/golden.json", "internal/testutil/fake.go", "pkg/testutils/x.go", "testhelpers/h.py",
		},
		false: {
			"testing.go", "contest.py", "latest.go", "Test.java", "IT.java", "Tests.kt", "src/attestation/a.go",
			"a_test.txt", "README_test.md", "testutils.go", "a.go", "main.py", "app.ts", "Foo.java", "Foo.kt",
			"Foo.cs", "foo.rb", "pkg/src/a.go", "FooIT.kt", "jest.config.js", "spec.go", "testdata.go",
		},
	} {
		for _, p := range paths {
			if IsTestFile(p) != want {
				t.Errorf("IsTestFile(%q) = %v, want %v", p, !want, want)
			}
		}
	}
}
