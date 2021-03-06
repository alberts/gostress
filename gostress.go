package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path"
	"runtime"
	"strings"
	"strconv"
	"io/ioutil"
	"sort"
	"time"
	"syscall"
)

var (
	GOROOT string
)

var disabledPackages = map[string]bool{
	//FAIL: go/parser.TestParse4... might be a testdata issue
	"go/parser": true,
	// Watcher.Watch() failed: inotify_add_watch: no such file or directory
	"os/inotify": true,
	//FAIL: smtp.TestBasic: Expected AUTH supported
	"smtp": true,
	//FAIL: mime.TestType
	"mime": true,
	//panic: Reuse of exported var name: requests
	"expvar": true,
	//signal was SIGCHLD: child status has changed, want SIGHUP: terminal line hangup
	"os/signal": true,
	//template.TestAll: unexpected write error: open _test/test.tmpl: no such file or directory
	"template": true,
	//FAIL: syslog.TestWrite: s.Info() = '""', but wanted '"<3>syslog_test: write test\n"'
	"syslog": true,
	//panic: gob: registering duplicate types for *gob.interfaceIndirectTestT
	"gob": true,
}

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
	for _, pkgDir := range pkgDirs {
		pkgName := packageName(pkgDir)
		pkgPrefix, _ := path.Split(pkgName)
		pkgPrefix = path.Clean(pkgPrefix)
		testPkgDir := path.Join(testRoot, "pkg", runtime.GOOS+"_"+runtime.GOARCH, pkgPrefix)
		err := os.MkdirAll(testPkgDir, 0764)
		if err != nil {
			return err
		}
		for _, pkg := range findPackages(path.Join(pkgDir, "_test")) {
			newPkg := path.Join(testPkgDir, path.Base(pkg))
			err = copyFile(newPkg, pkg)
			if err != nil {
				return err
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

		pkgName := packageName(pkgDir)
		if _, ok := disabledPackages[pkgName]; ok {
			fmt.Fprintf(os.Stderr, "SKIPPING DISABLED PACKAGE: %s\n", pkgName)
			continue
		}
		pkgParts := strings.Split(pkgName, "/", -1)

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
				parts := strings.Split(str, ".", 2)
				if parts[0] != pkgParts[len(pkgParts)-1] {
					fmt.Fprintf(os.Stderr, "SKIPPING PACKAGE WITH EXTERNAL TESTS: %s\n", str)
					continue
				}
				if name == "tests" {
					tests = append(tests, parts[1])
				} else {
					benchmarks = append(benchmarks, parts[1])
				}
			}
		}
		if len(tests) == 0 && len(benchmarks) == 0 {
			continue
		}
		testMains = append(testMains, &TestMain{pkgName, tests, benchmarks})
	}
	return testMains, nil
}

func writeSingleTest(testMain *TestMain, testName string, testType int, filename string) os.Error {

	src := bytes.NewBufferString("")

	fmt.Fprint(src, "// "+testMain.pkgName+"."+testName+"\n")

	fmt.Fprint(src, "//\n")
	fmt.Fprint(src, "package main\n\n")
	fmt.Fprint(src, "import \"sync\"\n")
	fmt.Fprint(src, "import \"testing\"\n")
	if testMain.pkgName != "regexp" {
		fmt.Fprint(src, "import \"regexp\"\n")
	}
	fmt.Fprintf(src, "import %s \"%s\"\n", testMain.underscorePkgName(), testMain.pkgName)
	fmt.Fprint(src, "\nfunc main() {\n")
	fmt.Fprint(src, "wg := new(sync.WaitGroup)\n")
	pkgName := testMain.underscorePkgName()

	fmt.Fprintf(src, "for i := 0; i < %d; i++ {\n", iters)

	fmt.Fprint(src, "wg.Add(1)\n")
	fmt.Fprint(src, "go func() {\n")
	if testType == 0 {
		fmt.Fprint(src, "tests := []testing.InternalTest{\n")
		testFunc := pkgName + "." + testName
		fmt.Fprintf(src, "{\"%s\", %s},\n", testMain.pkgName+"."+testName, testFunc)
		fmt.Fprint(src, "}\n")
		fmt.Fprint(src, "testing.Main(regexp.MatchString, tests)\n")
	} else if testType == 1 {
		fmt.Fprint(src, "benchmarks := []testing.InternalBenchmark{\n")
		benchFunc := pkgName + "." + testName
		fmt.Fprintf(src, "{\"%s\", %s},\n", testMain.pkgName+"."+testName, benchFunc)
		fmt.Fprint(src, "}\n")
		fmt.Fprint(src, "testing.RunBenchmarks(regexp.MatchString, benchmarks)\n")
	}

	fmt.Fprint(src, "wg.Done()\n")
	fmt.Fprint(src, "}()\n")

	fmt.Fprint(src, "}\n\n")
	fmt.Fprint(src, "wg.Wait()\n")
	fmt.Fprint(src, "}\n")

	//fmt.Printf("%s\n", string(src.Bytes()))

	file, err := os.Open(filename, os.O_CREAT|os.O_TRUNC|os.O_WRONLY, 0666)

	if err != nil {
		return err
	}
	defer file.Close()

	fileset := token.NewFileSet()

	fileNode, err := parser.ParseFile(fileset, filename, src.Bytes(), parser.ParseComments)
	if err != nil {
		panic(err)
	}

	config := printer.Config{printer.TabIndent, 8}
	_, err = config.Fprint(file, fileset, fileNode)
	if err != nil {
		return err
	}
	return nil
}


func executeSingleTest(test string) os.Error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	origEnv := os.Getenv("GOROOT")

	//Set up compiler and linker location
	var compiler, linker string
	var compilerExec, linkerExec string

	gobinArch := os.Getenv("GOARCH")
	if gobinArch == "amd64" {
		compilerExec = "6g"
		linkerExec = "6l"
	} else {
		compilerExec = "8g"
		linkerExec = "8l"
	}

	//If GOBIN is defined rather use that
	gobinEnv := os.Getenv("GOBIN")
	if gobinEnv != "" {
		compiler = gobinEnv + "/" + compilerExec
		linker = gobinEnv + "/" + linkerExec
	} else {
		compiler = origEnv + "/bin/" + compilerExec
		linker = origEnv + "/bin/" + linkerExec
	}

	myProcess, err := os.StartProcess(compiler, []string{"", "-e", "-o", test + ".6", test}, []string{"GOROOT=" + cwd + "/go.gostress", "GOMAXPROCS=" + strconv.Itoa(gomaxproc)}, ".", nil)

	if err != nil {
		return err
	}
	waitMsg, err := myProcess.Wait(0)
	if err != nil {
		return err
	}
	if waitMsg.ExitStatus() != 0 {
		return os.NewError("did not compile")
	}

	//fmt.Printf ("\nCompiled\n")

	myProcess, err = os.StartProcess(linker, []string{"", "-o", "work/test", test + ".6"}, []string{"GOROOT=" + cwd + "/go.gostress", "GOMAXPROCS=" + strconv.Itoa(gomaxproc)}, ".", nil)

	//myProcess, err = os.StartProcess("./myTest", []string{"-o test", test + ".6"},nil, ".", []*os.File {os.Stdin, os.Stdout, os.Stderr})
	if err != nil {
		return err
	}
	waitMsg, err = myProcess.Wait(0)
	if err != nil {
		return err
	}
	if waitMsg.ExitStatus() != 0 {
		return os.NewError("did not link")
	}
	//fmt.Printf ("\nLinked\n")

	errLog, err := os.Open(test+".output", os.O_WRONLY|os.O_CREAT|os.O_TRUNC, 0666)
	if err != nil {
		panic(err)
	}
	defer errLog.Close()

	var procResp *os.Process
	response := make(chan bool)
	processChan := make(chan *os.Process)
	if timeout > 0 {
		ticker := time.NewTicker(timeout * 1000000000)
		var boolResp bool
		go pushTest(cwd, response, errLog, processChan)
		procResp = <-processChan
		select {
		case boolResp = <-response:
			if boolResp == false {
				return os.NewError("Test case did not return normal")
			}
		case <-ticker.C:
			errLog.WriteString("GOSTRESS TIMEOUT!!!\n")
			syscall.Kill(procResp.Pid, syscall.SIGQUIT)
			return os.NewError("Test case timeout")
		}
	} else {
		go pushTest(cwd, response, errLog, processChan)
		procResp = <-processChan
		select {
		case boolResp := <-response:
			if boolResp == false {
				return os.NewError("Test case did not return normal")
			}
		}
	}

	//process went smoothly

	err = os.Remove(test + ".output")
	if err != nil {
		panic(err)
	}
	return nil
}

func pushTest(cwd string, response chan bool, errLog *os.File, processChan chan *os.Process) {
	myProcess, err := os.StartProcess("test", []string{"test"}, []string{"PATH=" + os.Getenv("PATH"), "GOROOT=" + cwd + "/go.gostress", "GOMAXPROCS=" + strconv.Itoa(gomaxproc)}, "./work", []*os.File{os.Stdin, nil, errLog})
	if err != nil {
		processChan <- nil
		response <- false
		return
	}
	processChan <- myProcess
	waitMsg, err := myProcess.Wait(0)
	if err != nil {
		response <- false
		return
	}
	if waitMsg.ExitStatus() != 0 {
		response <- false
		return
	}
	response <- true
}


func writePackageTest(filename string, testMain *TestMain) os.Error {
	src := bytes.NewBufferString("")
	fmt.Fprint(src, "// "+testMain.pkgName+".head\n")

	fmt.Fprint(src, "//\n")
	fmt.Fprint(src, "package main\n\n")
	fmt.Fprint(src, "import \"sync\"\n")
	fmt.Fprint(src, "import \"testing\"\n")
	if testMain.pkgName != "regexp" {
		fmt.Fprint(src, "import \"regexp\"\n")
	}
	fmt.Fprintf(src, "import %s \"%s\"\n", testMain.underscorePkgName(), testMain.pkgName)
	fmt.Fprint(src, "func main() {\n")
	fmt.Fprint(src, "wg := new(sync.WaitGroup)\n")

	pkgName := testMain.underscorePkgName()

	fmt.Fprint(src, "tests := []testing.InternalTest{\n")
	for _, test := range testMain.tests {
		testFunc := pkgName + "." + test
		fmt.Fprintf(src, "{\"%s\", %s},\n", testMain.pkgName+"."+test, testFunc)
	}
	fmt.Fprint(src, "}\n")
	fmt.Fprint(src, "benchmarks := []testing.InternalBenchmark{\n")
	for _, bench := range testMain.benchmarks {
		benchFunc := pkgName + "." + bench
		fmt.Fprintf(src, "{\"%s\", %s},\n", testMain.pkgName+"."+bench, benchFunc)
	}
	fmt.Fprint(src, "}\n")
	fmt.Fprintf(src, "for i := 0; i < %d; i++ {\n", iters)
	fmt.Fprint(src, "wg.Add(1)\n")
	fmt.Fprint(src, "go func() {\n")
	fmt.Fprint(src, "testing.Main(regexp.MatchString, tests)\n")
	fmt.Fprint(src, "wg.Done()\n")
	fmt.Fprint(src, "}()\n")
	fmt.Fprint(src, "wg.Add(1)\n")
	fmt.Fprint(src, "go func() {\n")
	fmt.Fprint(src, "testing.RunBenchmarks(regexp.MatchString, benchmarks)\n")
	fmt.Fprint(src, "wg.Done()\n")
	fmt.Fprint(src, "}()\n")

	fmt.Fprint(src, "}\n\n")

	fmt.Fprint(src, "wg.Wait()\n")
	fmt.Fprint(src, "}\n")

	file, err := os.Open(filename, os.O_CREAT|os.O_TRUNC|os.O_WRONLY, 0666)

	if err != nil {
		return err
	}
	defer file.Close()

	//fmt.Printf("%s\n", string(src.Bytes()))

	fileset := token.NewFileSet()

	fileNode, err := parser.ParseFile(fileset, filename, src.Bytes(), parser.ParseComments)
	if err != nil {
		panic(err)
	}

	config := printer.Config{printer.TabIndent, 8}
	_, err = config.Fprint(file, fileset, fileNode)
	if err != nil {
		return err
	}
	return nil
}


func loadBlackList() []string {
	file, err := os.Open("blacklist", os.O_RDONLY, 0764)
	if err != nil {
		fmt.Printf("Could not find blacklist\n")
		return []string{""}
	}

	var character [1]byte

	count := 0
	done := false
	for !done {
		switch nr, er := file.Read(character[:]); true {
		case nr == 0:
			count = count + 1
			done = true
			break
		case er != nil:
			panic(er)
		case nr > 0:
			if character[0] == '\n' {
				count = count + 1
			}
		}
	}

	if count == 0 {
		return nil
	}

	var result = make([]string, count)

	name := ""
	count = 0

	file.Close()

	file, err = os.Open("blacklist", os.O_RDONLY, 0764)
	if err != nil {
		fmt.Printf("Could not open blacklist\n")
		//panic (err)
	}

	done = false
	for !done {
		switch nr, er := file.Read(character[:]); true {
		case nr == 0:
			if name != "" {
				result[count] = name
			}
			done = true
			break
		case er != nil:
			panic(er)
		case nr > 0:
			if character[0] == '\n' {
				result[count] = name
				count = count + 1
				name = ""
			} else {
				name = name + string(character[0])
			}
		}
	}

	file.Close()

	return result

}

func listContains(list []string, word string) bool {
	for _, s := range list {
		if word == s {
			return true
		}
	}
	return false
}

const (
	TEST      string = "TEST"
	BENCHMARK string = "BENCHMARK"
	PACKAGE   string = "PACKAGE"
)

func runTest(testMain *TestMain, testName string, typeOfTest string, testCount int, blackList []string, nthTime int) bool {
	var fullName, filename string
	if typeOfTest == PACKAGE {
		filename = "pTest" + testMain.underscorePkgName() + "_" + strconv.Itoa(nthTime) + ".go"
		fullName = testMain.pkgName + ".head"
	} else {
		filename = strings.Join([]string{"sTest", testMain.underscorePkgName(), "", strconv.Itoa(testCount), "_", strconv.Itoa(nthTime), ".go"}, "")
		fullName = testMain.pkgName + "." + testName
	}

	fmt.Printf("%s", fullName)
	if listContains(blackList, fullName) || listContains(blackList, testMain.pkgName) {
		fmt.Printf(", skipped\n")
		return true
	}

	var err os.Error
	switch typeOfTest {
	case TEST:
		err = writeSingleTest(testMain, testName, 0, filename)
	case BENCHMARK:
		err = writeSingleTest(testMain, testName, 1, filename)
	case PACKAGE:
		err = writePackageTest(filename, testMain)
	}
	if err != nil {
		panic(err)
	}

	err = executeSingleTest(filename)
	if err != nil {
		//panic (err)
		fmt.Printf(", failed\n")
		return false
	} else {
		fmt.Printf(", passed\n")
		return true
	}
	return false
}

func generateSurvey(testMains []*TestMain) os.Error {

	fmt.Printf("SURVEY START\n")

	blackList := loadBlackList()
	fmt.Printf("blackList: %s\n", blackList)
	resultFile, err := os.Open("result.file", os.O_RDWR|os.O_CREAT|os.O_TRUNC, 0764)
	if err != nil {
		panic(err)
	}

	for _, testMain := range testMains {
		testCount := 0
		for _, test := range testMain.tests {
			failures := 0
			for i := 0; i < reruns; i++ {
				result := runTest(testMain, test, TEST, testCount, blackList, i)
				if result == false {
					failures++
				}
			}
			testCount = testCount + 1
			resultFile.WriteString(test + ":" + strconv.Itoa(failures) + "\n")
		}
		for _, benchmark := range testMain.benchmarks {
			failures := 0
			for i := 0; i < reruns; i++ {
				result := runTest(testMain, benchmark, BENCHMARK, testCount, blackList, i)
				if result == false {
					failures++
				}
			}
			testCount = testCount + 1
			resultFile.WriteString(benchmark + ":" + strconv.Itoa(failures) + "\n")
		}
		failures := 0
		for i := 0; i < reruns; i++ {
			result := runTest(testMain, "", PACKAGE, 0, blackList, i)
			if result == false {
				failures++
			}
		}
		resultFile.WriteString(testMain.pkgName + ":" + strconv.Itoa(failures) + "\n")
	}
	resultFile.Close()
	fmt.Printf("SURVEY DONE\n")
	return nil
}

type MapEntry struct {
	key   string
	value setTestRecord
}

type MapEntryArray []MapEntry

func (arr MapEntryArray) Len() int {
	return len(arr)
}

func (arr MapEntryArray) Less(i, j int) bool {
	return arr[i].key < arr[j].key
}

func (arr MapEntryArray) Swap(i, j int) {
	arr[i], arr[j] = arr[j], arr[i]
}

type testRecord struct {
	name         string
	failures     int
	failureFiles []string
	origFileName string
}

type setTestRecord map[string]testRecord

func generateReport() os.Error {

	dirName := "report"
	os.Mkdir(dirName, 0764)
	files, err := ioutil.ReadDir(".")

	if err != nil {
		return err
	}
	for _, f := range files {
		if !f.IsDirectory() {
			if len(f.Name) > 5 && f.Name[1:5] == "Test" && !strings.Contains(f.Name, ".6") {
				err = copyFile(dirName+"/"+f.Name, f.Name)
				if err != nil {
					panic(err)
				}
			}
		}
	}
	err = copyFile(dirName+"/blacklist", "blacklist")
	if err != nil {
		panic(err)
	}
	err = copyFile(dirName+"/result.file", "result.file")
	if err != nil {
		panic(err)
	}

	packageMap := make(map[string]setTestRecord)

	files, err = ioutil.ReadDir(dirName)
	if err != nil {
		return err
	}

	//sort.Sort (FileInfoArray(files))

	for _, f := range files {
		if !f.IsDirectory() && strings.HasSuffix(f.Name, "_0.go") {
			line, err := readFirstLine(dirName + "/" + f.Name)
			if err != nil {
				panic(err)
			}
			words := strings.Split(line, " ", -1)
			details := strings.Split(words[1], ".", -1)
			/*var outputFile bool
			_, err = os.Open(f.Name+".output", os.O_RDONLY, 0764)
			if err != nil {
				outputFile = false
			} else {
				outputFile = true
			}*/
			// >>package<<.Class
			set := packageMap[details[0]]
			if set == nil {
				set = make(setTestRecord)
			}
			// package.>>Class<<
			record := set[details[1]]
			/*if record == nil {
				record = &testRecord{}
			}*/
			record.name = details[1]
			record.origFileName = f.Name
			//fmt.Printf ("f.Name: %v\n", f.Name)
			coreFileName := f.Name[0:strings.LastIndex(f.Name, "_")]
			record.failures = 0
			for _, outputF := range files {
				if !outputF.IsDirectory() && strings.HasSuffix(outputF.Name, ".go.output") && strings.Contains(outputF.Name, coreFileName) {
					record.failures = record.failures + 1
					var contains bool = false
					for _, ent := range record.failureFiles {
						if ent == outputF.Name {
							contains = true
						}
					}
					if !contains {
						record.failureFiles = append(record.failureFiles, outputF.Name)
					}
				}
			}
			set[details[1]] = record
			packageMap[details[0]] = set
		}
	}

	//sort.Sort (packageMap)

	htmlFile, err := os.Open(dirName+"/index.html", os.O_RDWR|os.O_CREAT|os.O_TRUNC, 0764)

	if err != nil {
		return err
	}

	htmlFile.WriteString("<HTML>\n")
	htmlFile.WriteString("<body>\n")
	htmlFile.WriteString("<h1>Gostress Report</h1>\n")
	//fmt.Printf ("map\n%s\n", packageMap)

	htmlFile.WriteString("<a href=\"blacklist\">View Blacklist</a>\n")

	htmlFile.WriteString("<table width=\"400\">\n")

	packageMapArray := make([]MapEntry, len(packageMap))

	//packageMapArray[0] = new (MapEntry)
	i := 0
	for pack, detail := range packageMap {
		packageMapArray[i] = MapEntry{pack, detail}
		i = i + 1
	}

	sort.Sort(MapEntryArray(packageMapArray))

	count := 0
	for _, entry := range packageMapArray {
		pack := entry.key
		detail := entry.value
		//fmt.Printf("pack: %s, %s\n", pack, detail)
		htmlFile.WriteString("\t<tbody>\n")
		if containsFault(detail) {
			htmlFile.WriteString("\t\t<tr><td style=\"background-color: #FF0000\" width=\"50\"></td><td><a href=\"")
		} else {
			htmlFile.WriteString("\t\t<tr><td style=\"background-color: #00FF00\" width=\"50\"></td><td><a href=\"")
		}
		htmlFile.WriteString(strings.Replace(pack, "/", "_", -1))
		htmlFile.WriteString(".html\">")
		htmlFile.WriteString(pack)
		htmlFile.WriteString("</a>")
		htmlFile.WriteString("</td></tr>")

		htmlFile.WriteString("\t</tbody>\n\n")
		count = count + 1
	}

	htmlFile.WriteString("</table>\n")

	htmlFile.WriteString("</BODY></HTML>\n")

	htmlFile.Close()

	for _, entry := range packageMapArray {
		pack := entry.key
		detail := entry.value

		file, err := os.Open("report/"+strings.Replace(pack, "/", "_", -1)+".html", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0764)
		if err != nil {
			panic(err)
		}

		file.WriteString("<html>\n")
		file.WriteString("<body>\n")
		file.WriteString("<table>\n")

		for packName, packRecord := range detail {
			/*details := strings.Split(w, ",", -1)
			if len(details) < 2 {
				continue
			}*/

			/*file.WriteString("<tr><td style=\"background-color: ")
			if packRecord.failures > 0 {
				file.WriteString("#FF0000")
			} else {
				file.WriteString("#00FF00")
			}
			file.WriteString("\" width=\"50\"></td>")*/

			file.WriteString("<tr><td>\n")
			file.WriteString("<table width=\"100%\" height=\"100%\">")
			file.WriteString("<tr>\n")
			for i := 0; i < packRecord.failures; i++ {
				file.WriteString("<td style=\"background-color: ")
				file.WriteString("#FF0000")
				file.WriteString("\" width=\"10\"></td>")
			}
			for i := packRecord.failures; i < reruns; i++ {
				file.WriteString("<td style=\"background-color: ")
				file.WriteString("#00FF00")
				file.WriteString("\" width=\"10\"></td>")
			}
			file.WriteString("</tr>")
			file.WriteString("</table>")
			file.WriteString("</td>")

			file.WriteString("<td><a href=\"")
			file.WriteString(packRecord.origFileName)
			file.WriteString("\">")
			file.WriteString(packName)
			file.WriteString("</a>")
			//if len(details) == 3 {
			for i := 0; i < packRecord.failures; i++ {
				file.WriteString("...<a href=\"")
				file.WriteString(packRecord.failureFiles[i])
				file.WriteString("\">output" + strconv.Itoa(i) + "</a>")
			}
			file.WriteString("</td></tr>\n")
		}

		file.WriteString("</table>")
		file.WriteString("</body>")
		file.WriteString("</html>")

		file.Close()
	}

	return nil
}

func containsFault(detail setTestRecord) bool {
	for _, v := range detail {
		if v.failures > 0 {
			return true
		}
	}
	return false
}

func readFirstLine(fileName string) (string, os.Error) {
	file, err := os.Open(fileName, os.O_RDONLY, 0764)
	if err != nil {
		return "", err
	}

	str := ""
	var buff [1]byte
	file.Read(buff[:])
	for buff[0] != '\n' {
		str = strings.Join([]string{str, string(buff[0])}, "")
		file.Read(buff[:])
	}
	file.Close()

	return str, nil
}

func generateRunner(filename string, testMains []*TestMain) os.Error {
	src := bytes.NewBufferString("")
	fmt.Fprint(src, "package main\n\n")
	fmt.Fprint(src, "import \"sync\"\n")
	fmt.Fprint(src, "import \"testing\"\n")
	fmt.Fprint(src, "import (\n")
	for _, testMain := range testMains {
		name := testMain.underscorePkgName()
		fmt.Fprintf(src, "%s \"%s\"\n", name, testMain.pkgName)
	}
	fmt.Fprint(src, ")\n")
	fmt.Fprint(src, "func main() {\n")
	fmt.Fprint(src, "wg := new(sync.WaitGroup)\n")
	for _, testMain := range testMains {
		pkgName := testMain.underscorePkgName()
		fmt.Fprint(src, "wg.Add(1)\n")
		fmt.Fprint(src, "go func() {\n")
		fmt.Fprint(src, "tests := []testing.InternalTest{\n")
		for _, test := range testMain.tests {
			testFunc := pkgName + "." + test
			fmt.Fprintf(src, "{\"%s\", %s},\n", testMain.pkgName+"."+test, testFunc)
		}
		fmt.Fprint(src, "}\n")
		fmt.Fprint(src, "benchmarks := []testing.InternalBenchmark{\n")
		for _, bench := range testMain.benchmarks {
			benchFunc := pkgName + "." + bench
			fmt.Fprintf(src, "{\"%s\", %s},\n", testMain.pkgName+"."+bench, benchFunc)
		}
		fmt.Fprint(src, "}\n")
		fmt.Fprintf(src, "for i := 0; i < %d; i++ {\n", iters)
		fmt.Fprint(src, "testing.Main(regexp.MatchString, tests)\n")
		fmt.Fprint(src, "testing.RunBenchmarks(regexp.MatchString, benchmarks)\n")
		fmt.Fprint(src, "}\n")
		fmt.Fprint(src, "wg.Done()\n")
		fmt.Fprint(src, "}()\n\n")
	}
	fmt.Fprint(src, "wg.Wait()\n")
	fmt.Fprint(src, "}\n")

	file, err := os.Open(filename, os.O_CREAT|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer file.Close()

	//fmt.Printf("%s\n", string(src.Bytes()))

	fileNode, err := parser.ParseFile(token.NewFileSet(), filename, src.Bytes(), 0)
	if err != nil {
		panic(err)
	}

	config := printer.Config{printer.TabIndent, 8}
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
		panic("Test would overwrite GOROOT")
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

	if mode == RUNNER {
		err = generateRunner("go.go", testMains)
		if err != nil {
			panic(err)
		}
	} else if mode == SURVEY {
		err = generateSurvey(testMains)
		if err != nil {
			panic(err)
		}
		err = generateReport()
		if err != nil {
			panic(err)
		}
	} else {
		fmt.Printf("No valid mode selected\n")
	}
}

var iters int
var mode string
var timeout int64
var gomaxproc int
var reruns int

const (
	RUNNER string = "runner"
	SURVEY string = "survey"
)

func init() {
	flag.IntVar(&iters, "iters", 100, "iterations per goroutine")
	flag.StringVar(&mode, "mode", RUNNER, "mode of operation, either \"runner\" or \"survey\"")
	flag.Int64Var(&timeout, "timeout", 600, "timeout for each individual test (seconds)")
	flag.IntVar(&gomaxproc, "gomaxproc", 10, "set GOMAXPROC value during testing")
	flag.IntVar(&reruns, "reruns", 10, "set amount by which each test must be rerun")
	flag.Parse()
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
