#!/bin/sh
set -xe

rm -rf go.gostress testdata
rm -rf output/*
rm -rf *.output

make nuke
make

mkdir -p go.gostress
cp -a $GOROOT/pkg go.gostress
find $GOROOT/src/pkg -name 'testdata' -type d | xargs -I DIR cp -a -i DIR .

./gostress -iters=100

rm -rf *.go.6

GOROOT=`pwd`/go.gostress 6g -e -o go.6 go.go
GOROOT=`pwd`/go.gostress 6l -o go go.6

GOMAXPROCS=1 ./go -v=true -benchmarks=.

#this explodes quite quickly...
#GOMAXPROCS=10 ./go -v=true -benchmarks=.
