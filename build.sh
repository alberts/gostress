#!/bin/sh
set -xe
rm -rf go.gostress testdata
make nuke
make
mkdir -p go.gostress
cp -a $GOROOT/pkg go.gostress
find $GOROOT/src/pkg -name 'testdata' -type d | xargs -I DIR cp -a DIR .
./gostress
GOROOT=`pwd`/go.gostress 6g -o go.6 go.go
GOROOT=`pwd`/go.gostress 6l -o go go.6
