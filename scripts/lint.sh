#!/bin/sh
golangci-lint run --color=always --enable-all --disable=wsl,funlen,exportloopref,gomnd,execinquery,godox,lll,gochecknoglobals,depguard,cyclop,gosmopolitan,nlreturn,varnamelen,nestif,musttag,mnd,tagliatelle,gocognit,gocyclo,maintidx
