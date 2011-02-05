package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path"
	"runtime"
	"strings"
)

var (
	GOROOT string
)

type pkgDirsVisitor struct {
	pkgDirs []string
}

func (v *pkgDirsVisitor) VisitDir(pathName string, f *os.FileInfo) bool {
	if path.Base(pathName) == "_test" {
		pkgDir, _ := path.Split(pathName)
		v.pkgDirs = append(v.pkgDirs, path.Clean(pkgDir))
	}
	return true
}

func (v *pkgDirsVisitor) VisitFile(pathName string, f *os.FileInfo) {}

type packagesVisitor struct {
	packages []string
}

func (v *packagesVisitor) VisitDir(pathName string, f *os.FileInfo) bool {
	return true
}

func (v *packagesVisitor) VisitFile(pathName string, f *os.FileInfo) {
	if strings.HasSuffix(pathName, ".a") {
		v.packages = append(v.packages, pathName)
	}
}

func findPackageDirs() []string {
	srcPkg := path.Join(GOROOT, "src", "pkg")
	v := new(pkgDirsVisitor)
	path.Walk(srcPkg, v, nil)
	return v.pkgDirs
}

func findPackages(dir string) []string {
	v := new(packagesVisitor)
	path.Walk(dir, v, nil)
	return v.packages
}

func copyFile(dest, src string) os.Error {
	srcFile, err := os.Open(src, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}
	destFile, err := os.Open(dest, os.O_CREAT|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		return err
	}
	defer destFile.Close()
	buf := make([]byte, int(srcInfo.Size))
	for {
		n, err := srcFile.Read(buf)
		if err != nil {
			if err == os.EOF {
				break
			}
			return err
		}
		n2, err := destFile.Write(buf[:n])
		if err != nil {
			return err
		}
		if n != n2 {
			panic("short write")
		}
	}
	return nil
}

func packageName(pkgDir string) string {
	srcPkg := path.Join(GOROOT, "src", "pkg")
	return pkgDir[len(srcPkg)+1:]
}

func copyTestPackages(testRoot string, pkgDirs []string) os.Error {
	// TODO check for GOBIN in the environment
	gopack := path.Join(GOROOT, "bin", "gopack")
	for _, pkgDir := range pkgDirs {
		pkgName := packageName(pkgDir)
		pkgPrefix, _ := path.Split(pkgName)
		pkgPrefix = path.Clean(pkgPrefix)
		testPkgDir := path.Join(testRoot, "pkg", runtime.GOOS+"_"+runtime.GOARCH, pkgPrefix)
		err := os.MkdirAll(testPkgDir, 0777)
		if err != nil {
			return err
		}
		for _, pkg := range findPackages(path.Join(pkgDir, "_test")) {
			newPkg := path.Join(testPkgDir, path.Base(pkg))
			err = copyFile(newPkg, pkg)
			if err != nil {
				return err
			}
			// TODO make this work with other GOARCHes
			testObj := path.Join(pkgDir, "_gotest_.6")
			args := []string{"gopack", "grc", newPkg, testObj}

			fmt.Printf("%+v\n", args)

			p, err := os.StartProcess(gopack, args, os.Environ(), "", nil)
			if err != nil {
				return err
			}
			defer p.Release()
			waitmsg, err := p.Wait(0)
			if err != nil {
				return err
			}
			if waitmsg.ExitStatus() != 0 {
				return os.NewError("gopack failed")
			}
		}
	}
	return nil
}

type TestMain struct {
	pkgName           string
	tests, benchmarks []string
}

func (tm *TestMain) underscorePkgName() string {
	return strings.Replace(tm.pkgName, "/", "_", -1)
}

func parseTestMains(pkgDirs []string) ([]*TestMain, os.Error) {
	testMains := make([]*TestMain, 0)

	for _, pkgDir := range pkgDirs {
		testmain := path.Join(pkgDir, "_testmain.go")
		fileNode, err := parser.ParseFile(token.NewFileSet(), testmain, nil, 0)
		if err != nil {
			return nil, err
		}

		tests := make([]string, 0)
		benchmarks := make([]string, 0)

		for _, decl := range fileNode.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.VAR || len(genDecl.Specs) != 1 {
				continue
			}
			spec := genDecl.Specs[0].(*ast.ValueSpec)
			name := spec.Names[0].Name
			if name != "tests" && name != "benchmarks" {
				continue
			}
			elts := spec.Values[0].(*ast.CompositeLit).Elts
			for _, elt := range elts {
				val := elt.(*ast.CompositeLit).Elts[0].(*ast.BasicLit).Value
				str := string(val)
				str = str[1 : len(str)-1]
				str = strings.SplitAfter(str, ".", 2)[1]
				if name == "tests" {
					tests = append(tests, str)
				} else {
					benchmarks = append(benchmarks, str)
				}
			}
		}
		pkgName := packageName(pkgDir)
		if len(tests) == 0 && len(benchmarks) == 0 {
			continue
		}
		testMains = append(testMains, &TestMain{pkgName, tests, benchmarks})
	}
	return testMains, nil
}

func generateRunner(filename string, testMains []*TestMain) os.Error {
	src := bytes.NewBufferString("")

	fmt.Fprint(src, "package main\n\n")
	fmt.Fprint(src, "import \"testing\"\n\n")
	fmt.Fprint(src, "import (\n")
	for _, testMain := range testMains {
		name := testMain.underscorePkgName()
		fmt.Fprintf(src, "%s \"%s\"\n", name, testMain.pkgName)
	}
	fmt.Fprint(src, ")\n")

	fmt.Fprint(src, "func main() {\n")
	for _, testMain := range testMains {
		pkgName := testMain.underscorePkgName()
		fmt.Fprint(src, "go func() {\n")
		fmt.Fprint(src, "tests := []testing.InternalTest{\n")
		for _, test := range testMain.tests {
			testFunc := pkgName + "." + test
			fmt.Fprintf(src, "{\"%s\", %s},\n", testFunc, testFunc)
		}
		fmt.Fprint(src, "}\n")
		fmt.Fprint(src, "benchmarks := []testing.InternalBenchmark{\n")
		for _, bench := range testMain.benchmarks {
			benchFunc := pkgName + "." + bench
			fmt.Fprintf(src, "{\"%s\", %s},\n", benchFunc, benchFunc)
		}
		fmt.Fprint(src, "}\n")
		fmt.Fprint(src, "for {\n")
		fmt.Fprint(src, "testing.Main(regexp.MatchString, tests)\n")
		fmt.Fprint(src, "testing.RunBenchmarks(regexp.MatchString, benchmarks)\n")
		fmt.Fprint(src, "}\n")
		fmt.Fprint(src, "}()\n")
	}
	fmt.Fprint(src, "c := make(chan bool)\n")
	fmt.Fprint(src, "<-c\n")
	fmt.Fprint(src, "}\n")

	file, err := os.Open(filename, os.O_CREAT|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	//fmt.Printf("%s\n", string(src.Bytes()))

	fileNode, err := parser.ParseFile(token.NewFileSet(), filename, src.Bytes(), parser.ParseComments)
	if err != nil {
		panic(err)
	}

	config := printer.Config{printer.TabIndent, 8, nil}
	_, err = config.Fprint(file, token.NewFileSet(), fileNode)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	testRoot := path.Join(cwd, "go.gostress")
	if GOROOT == testRoot {
		panic("Test cannot overwrite GOROOT")
	}
	err = os.RemoveAll(testRoot)
	if err != nil {
		panic(err)
	}
	err = os.MkdirAll(testRoot, 0777)
	if err != nil {
		panic(err)
	}

	pkgDirs := findPackageDirs()

	err = copyTestPackages(testRoot, pkgDirs)
	if err != nil {
		panic(err)
	}

	testMains, err := parseTestMains(pkgDirs)
	if err != nil {
		panic(err)
	}

	err = generateRunner("go.go", testMains)
	if err != nil {
		panic(err)
	}

	osarch := runtime.GOOS + "_" + runtime.GOARCH
	gorootTesting := path.Join(GOROOT, "pkg", osarch, "testing.a")
	testRootTesting := path.Join(testRoot, "pkg", osarch, "testing.a")
	err = copyFile(testRootTesting, gorootTesting)
	if err != nil {
		panic(err)
	}
}

func init() {
	GOROOT = os.Getenv("GOROOT")
	if GOROOT == "" {
		panic("GOROOT not set in environment")
	}
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if !path.IsAbs(GOROOT) {
		GOROOT = path.Join(cwd, GOROOT)
	}
	GOROOT = path.Clean(GOROOT)
}
