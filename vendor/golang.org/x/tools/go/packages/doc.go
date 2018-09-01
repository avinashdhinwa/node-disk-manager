// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*

Package packages provides information about Go packages,
such as their path, source files, and imports.
It can optionally load, parse, and type-check the source files of a
package, and obtain type information for their dependencies either by
loading export data files produced by the Go compiler or by
recursively loading dependencies from source code.

THIS INTERFACE IS EXPERIMENTAL AND IS LIKELY TO CHANGE.

This package currently requires a go1.11 version of go list;
its functions will return a GoTooOldError for older toolchains.

This package is intended to replace golang.org/x/tools/go/loader.
It provides a simpler interface to the same functionality and serves
as a foundation for analysis tools that work with 'go build',
including its support for versioned packages,
and also with alternative build systems such as Bazel and Blaze.

Its primary operation is to load packages through
the Metadata, TypeCheck, and WholeProgram functions,
which accept a list of string arguments that denote
one or more packages according to the conventions
of the underlying build system.

For example, in a 'go build' workspace,
they may be a list of package names,
or relative directory names,
or even an ad-hoc list of source files:

	fmt
	encoding/json
	./json
	a.go b.go

For a Bazel project, the arguments use Bazel's package notation:

	@repo//project:target
	//project:target
	:target
	target

An application that loads packages can thus pass its command-line
arguments directly to the loading functions and it will integrate with the
usual conventions for that project.

The result of a call to a loading function is a set of Package
objects describing the packages denoted by the arguments.
These "initial" packages are in fact the roots of a graph of Packages,
the import graph, that includes complete transitive dependencies.
Clients may traverse the import graph by following the edges in the
Package.Imports map, which relates the import paths that appear in the
package's source files to the packages they import.

Each package has three kinds of name: ID, PkgPath, and Name.
A package's ID is an unspecified identifier that uniquely
identifies it throughout the workspace, and thus may be used as a key in
a map of packages. Clients should not interpret this string, no matter
how intelligible it looks, as its structure varies across build systems.
A package's PkgPath is the name by which the package is known to the
compiler, linker, and runtime: it is the string returned by
reflect.Type.PkgPath or fmt.Sprintf("%T", x). The PkgPath is not
necessarily unique throughout the workspace; for example, an in-package
test has the same PkgPath as the package under test.
A package's Name is the identifier that appears in the "package"
declaration at the start of each of its source files,
and is the name declared when importing it into another file.
A package whose Name is "main" is linked as an executable.

The loader's three entry points, Metadata, TypeCheck, and
WholeProgram, provide increasing levels of detail.

Metadata returns only a description of each package,
its source files and imports.
Some build systems permit build steps to generate
Go source files that are then compiled.
The Packages describing such a program report
the locations of the generated files.
The process of loading packages invokes the
underlying build system to ensure that these
files are present and up-to-date.

Although 'go build' does not in general allow code generation,
it does in a limited form in its support for cgo.
For a package whose source files import "C", subjecting them to cgo
preprocessing, the loader reports the location of the pure-Go source
files generated by cgo. This too may entail a partial build.
Cgo processing is disabled for Metadata queries,
or when the DisableCgo option is set.

TypeCheck additionally loads, parses, and type-checks
the source files of the initial packages,
and exposes their syntax trees and type information.
Type information for dependencies of the initial
packages is obtained not from Go source code but from
compiler-generated export data files.
Again, loading invokes the underlying build system to
ensure that these files are present and up-to-date.

WholeProgram loads complete type information about
the initial packages and all of their transitive dependencies.

Example:

	pkgs, err := packages.TypeCheck(nil, flag.Args()...)
	if err != nil { ... }
	for _, pkg := range pkgs {
		...
	}

*/
package packages // import "golang.org/x/tools/go/packages"

/*

Motivation and design considerations

The new package's design solves problems addressed by two existing
packages: go/build, which locates and describes packages, and
golang.org/x/tools/go/loader, which loads, parses and type-checks them.
The go/build.Package structure encodes too much of the 'go build' way
of organizing projects, leaving us in need of a data type that describes a
package of Go source code independent of the underlying build system.
We wanted something that works equally well with go build and vgo, and
also other build systems such as Bazel and Blaze, making it possible to
construct analysis tools that work in all these environments.
Tools such as errcheck and staticcheck were essentially unavailable to
the Go community at Google, and some of Google's internal tools for Go
are unavailable externally.
This new package provides a uniform way to obtain package metadata by
querying each of these build systems, optionally supporting their
preferred command-line notations for packages, so that tools integrate
neatly with users' build environments. The Metadata query function
executes an external query tool appropriate to the current workspace.

Loading packages always returns the complete import graph "all the way down",
even if all you want is information about a single package, because the query
mechanisms of all the build systems we currently support ({go,vgo} list, and
blaze/bazel aspect-based query) cannot provide detailed information
about one package without visiting all its dependencies too, so there is
no additional asymptotic cost to providing transitive information.
(This property might not be true of a hypothetical 5th build system.)

This package provides no parse-but-don't-typecheck operation because most tools
that need only untyped syntax (such as gofmt, goimports, and golint)
seem not to care about any files other than the ones they are directly
instructed to look at.  Also, it is trivial for a client to supplement
this functionality on top of a Metadata query.

In calls to TypeCheck, all initial packages, and any package that
transitively depends on one of them, must be loaded from source.
Consider A->B->C->D->E: if A,C are initial, A,B,C must be loaded from
source; D may be loaded from export data, and E may not be loaded at all
(though it's possible that D's export data mentions it, so a
types.Package may be created for it and exposed.)

The old loader had a feature to suppress type-checking of function
bodies on a per-package basis, primarily intended to reduce the work of
obtaining type information for imported packages. Now that imports are
satisfied by export data, the optimization no longer seems necessary.

Despite some early attempts, the old loader did not exploit export data,
instead always using the equivalent of WholeProgram mode. This was due
to the complexity of mixing source and export data packages (now
resolved by the upward traversal mentioned above), and because export data
files were nearly always missing or stale. Now that 'go build' supports
caching, all the underlying build systems can guarantee to produce
export data in a reasonable (amortized) time.

Packages that are part of a test are marked IsTest=true.
Such packages include in-package tests, external tests,
and the test "main" packages synthesized by the build system.
The latter packages are reported as first-class packages,
avoiding the need for clients (such as go/ssa) to reinvent this
generation logic.

One way in which go/packages is simpler than the old loader is in its
treatment of in-package tests. In-package tests are packages that
consist of all the files of the library under test, plus the test files.
The old loader constructed in-package tests by a two-phase process of
mutation called "augmentation": first it would construct and type check
all the ordinary library packages and type-check the packages that
depend on them; then it would add more (test) files to the package and
type-check again. This two-phase approach had four major problems:
1) in processing the tests, the loader modified the library package,
   leaving no way for a client application to see both the test
   package and the library package; one would mutate into the other.
2) because test files can declare additional methods on types defined in
   the library portion of the package, the dispatch of method calls in
   the library portion was affected by the presence of the test files.
   This should have been a clue that the packages were logically
   different.
3) this model of "augmentation" assumed at most one in-package test
   per library package, which is true of projects using 'go build',
   but not other build systems.
4) because of the two-phase nature of test processing, all packages that
   import the library package had to be processed before augmentation,
   forcing a "one-shot" API and preventing the client from calling Load
   in several times in sequence as is now possible in WholeProgram mode.
   (TypeCheck mode has a similar one-shot restriction for a different reason.)

Early drafts of this package supported "multi-shot" operation
in the Metadata and WholeProgram modes, although this feature is not exposed
through the API and will likely be removed.
Although it allowed clients to make a sequence of calls (or concurrent
calls) to Load, building up the graph of Packages incrementally,
it was of marginal value: it complicated the API
(since it allowed some options to vary across calls but not others),
it complicated the implementation,
it cannot be made to work in TypeCheck mode, as explained above,
and it was less efficient than making one combined call (when this is possible).
Among the clients we have inspected, none made multiple calls to load
but could not be easily and satisfactorily modified to make only a single call.
However, applications changes may be required.
For example, the ssadump command loads the user-specified packages
and in addition the runtime package.  It is tempting to simply append
"runtime" to the user-provided list, but that does not work if the user
specified an ad-hoc package such as [a.go b.go].
Instead, ssadump no longer requests the runtime package,
but seeks it among the dependencies of the user-specified packages,
and emits an error if it is not found.

Overlays: the ParseFile hook in the API permits clients to vary the way
in which ASTs are obtained from filenames; the default implementation is
based on parser.ParseFile. This features enables editor-integrated tools
that analyze the contents of modified but unsaved buffers: rather than
read from the file system, a tool can read from an archive of modified
buffers provided by the editor.
This approach has its limits. Because package metadata is obtained by
fork/execing an external query command for each build system, we can
fake only the file contents seen by the parser, type-checker, and
application, but not by the metadata query, so, for example:
- additional imports in the fake file will not be described by the
  metadata, so the type checker will fail to load imports that create
  new dependencies.
- in TypeCheck mode, because export data is produced by the query
  command, it will not reflect the fake file contents.
- this mechanism cannot add files to a package without first saving them.

Questions & Tasks

- Add this pass-through option for the underlying query tool:
     Flags   []string

- Add GOARCH/GOOS?
  They are not portable concepts, but could be made portable.
  Our goal has been to allow users to express themselves using the conventions
  of the underlying build system: if the build system honors GOARCH
  during a build and during a metadata query, then so should
  applications built atop that query mechanism.
  Conversely, if the target architecture of the build is determined by
  command-line flags, the application can pass the relevant
  flags through to the build system using a command such as:
    myapp -query_flag="--cpu=amd64" -query_flag="--os=darwin"
  However, this approach is low-level, unwieldy, and non-portable.
  GOOS and GOARCH seem important enough to warrant a dedicated option.

- Build tags: where do they fit in?  How does Bazel/Blaze handle them?

- How should we handle partial failures such as a mixture of good and
  malformed patterns, existing and non-existent packages, succesful and
  failed builds, import failures, import cycles, and so on, in a call to
  Load?

- Do we need a GeneratedBy map that maps the name of each generated Go
  source file in Srcs to that of the original file, if known, or "" otherwise?
  Or are //line directives and "Generated" comments in those files enough?

- Support bazel, blaze, and go1.10 list, not just go1.11 list.

- Support a "contains" query: a boolean option would cause the the
  pattern words to be interpreted as filenames, and the query would
  return the package(s) to which the file(s) belong.

- Handle (and test) various partial success cases, e.g.
  a mixture of good packages and:
  invalid patterns
  nonexistent packages
  empty packages
  packages with malformed package or import declarations
  unreadable files
  import cycles
  other parse errors
  type errors
  Make sure we record errors at the correct place in the graph.

- Missing packages among initial arguments are not reported.
  Return bogus packages for them, like golist does.

- "undeclared name" errors (for example) are reported out of source file
  order. I suspect this is due to the breadth-first resolution now used
  by go/types. Is that a bug? Discuss with gri.

- https://github.com/golang/go/issues/25980 causes these commands to crash:
  $ GOPATH=/none ./gopackages -all all
  due to:
  $ GOPATH=/none go list -e -test -json all
  and:
  $ go list -e -test ./relative/path

- Modify stringer to use go/packages, perhaps initially under flag control.

- Bug: "gopackages fmt a.go" doesn't produce an error.

*/