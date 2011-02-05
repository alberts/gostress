package main

import (
	"fmt"
	"os"
	"path"
	"strings"
)

var (
	GOROOT string
)

type pkgDirsVisitor struct{
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

type packagesVisitor struct{
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
	destFile, err := os.Open(dest, os.O_CREAT | os.O_WRONLY | os.O_TRUNC, 0666)
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

func copyTestPackages(testRoot string, pkgDirs []string) os.Error {
	srcPkg := path.Join(GOROOT, "src", "pkg")
	for _, pkgDir := range pkgDirs {
		pkgName := pkgDir[len(srcPkg)+1:]
		pkgPrefix, _ := path.Split(pkgName)
		pkgPrefix = path.Clean(pkgPrefix)
		testPkgDir := path.Join(testRoot, "pkg", pkgPrefix)
		err := os.MkdirAll(testPkgDir, 0777)
		if err != nil {
			return err
		}
		for _, pkg := range findPackages(path.Join(pkgDir, "_test")) {
			err = copyFile(path.Join(testPkgDir, path.Base(pkg)), pkg)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func parseTestMains(pkgDirs []string) os.Error {
	for _, pkgDir := range pkgDirs {
		testmain := path.Join(pkgDir, "_testmain.go")


		_ = testmain
		break
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

	err = parseTestMains(pkgDirs)
	if err != nil {
		panic(err)
	}
}

func init() {
	_ = fmt.Printf
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
