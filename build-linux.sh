#!/bin/bash
cd source
go get virus
go build -tags "containers_image_openpgp exclude_graphdriver_btrfs"
