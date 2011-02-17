GOROOT=`pwd`/go.gostress 6g -e -o go.6 $1
GOROOT=`pwd`/go.gostress 6l -o go go.6

GOMAXPROCS=$2 ./go

