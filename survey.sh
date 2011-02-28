#!/bin/sh
set -xe

rm -rf go.gostress testdata
rm -rf work
rm -rf output/*

make nuke
make

mkdir -p go.gostress
mkdir -p work
cp -a $GOROOT/pkg go.gostress
find $GOROOT/src/pkg -name 'testdata' -type d | xargs -I DIR cp -a DIR .
cp -r testdata work/
cp -r go.gostress work/

./gostress -iters=10 -gomaxproc=10 -mode="survey" -timeout=180 -reruns=3

rm -rf *.go.6

rm -rf sTest*
rm -rf pTest*
rm -rf *.output

#GOROOT=`pwd`/go.gostress 6g -e -o go.6 sTestxml20.go
#GOROOT=`pwd`/go.gostress 6l -o go go.6

#GOMAXPROCS=1 ./go -v=true -benchmarks=.

#this explodes quite quickly...
#GOMAXPROCS=10 ./go -v=true -benchmarks=.
