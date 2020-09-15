#!/bin/bash
# Copyright 2020 syzkaller project authors. All rights reserved.
# Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

# This script generates sys/linux/test/syz_mount_image_btrfs_* files.

set -eu

# Currently disabled
# declare -a Op1=("-d raid0 " "-d raid1 " "-d raid5 " "-d raid6 " "-d raid10 " "-d single " "-d dup ")
declare -a Op1=("-M " "")
declare -a Op2=("-O mixed-bg --nodesize 4096 " "-O extref " "-O raid56 " "-O no-holes " "-O raid1c34 ")
declare -a Op3=("-K " "")
declare -a Op4=("--csum crc32c " "--csum xxhash " "--csum sha256 " "--csum blake2 ")
declare -i dex=0

dir=`dirname $0`
echo $dir

for op1 in "${Op1[@]}"; do
	for op2 in "${Op2[@]}"; do
		for op3 in "${Op3[@]}"; do
			for op4 in "${Op4[@]}"; do
				for size in 16M 32M 64M 128M; do
					echo mkfs.btrfs ${op1}${op2}${op3}${op4} disk.raw ${size}
					rm -f disk.raw
					fallocate -l ${size} disk.raw
					err=""
					mkfs.btrfs ${op1}${op2}${op3}${op4} disk.raw >/dev/null || err="1"
					if [ "$err" != "" ]; then
						if [ "$size" == "128M" ]; then
							exit 1
						fi
						continue
					fi
					out="$dir/../sys/linux/test/syz_mount_image_btrfs_$dex"
					echo "# Code generated by tools/create_f2fs_image.sh. DO NOT EDIT." > $out
					echo "# requires: manual" >> $out
					echo >> $out
					go run "$dir/syz-imagegen/imagegen.go" -image=./disk.raw -fs=btrfs >> $out
					dex=dex+1
					break
				done
			done
		done
	done
done
rm -f disk.raw