#!/bin/sh
set -xe

rm -rf go.gostress testdata
rm -rf work
rm -rf output/*
rm -rf *.output

make nuke
make

mkdir -p go.gostress
mkdir -p work
cp -a $GOROOT/pkg go.gostress
find $GOROOT/src/pkg -name 'testdata' -type d | xargs -I DIR cp -a DIR .
cp -r testdata work/
cp -r go.gostress work/

./gostress -iters=1000 -gomaxproc=100 -mode="survey" -timeout=3600

rm -rf *.go.6

rm -rf sTest*
rm -rf pTest*

#GOROOT=`pwd`/go.gostress 6g -e -o go.6 sTestxml20.go
#GOROOT=`pwd`/go.gostress 6l -o go go.6

#GOMAXPROCS=1 ./go -v=true -benchmarks=.

#this explodes quite quickly...
#GOMAXPROCS=10 ./go -v=true -benchmarks=.
