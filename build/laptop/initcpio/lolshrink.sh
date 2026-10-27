#!/bin/sh
# lolssd laptop: shrink the root ext4 and its partition in the initramfs, before /sysroot is
# mounted (ext4 cannot shrink while mounted). Kernel parameter: lolssd.shrink=<part>:<sectors>,
# e.g. lolssd.shrink=nvme0n1p2:683544576. Filesystem first, then the partition, so a failure
# at any step leaves a filesystem no bigger than its partition. Always exits 0: boot goes on.
# English only: the initramfs console font may lack Cyrillic.
say() { echo "lolssd-shrink: $*"; }

arg=$(sed -n 's/.*lolssd\.shrink=\([^ ]*\).*/\1/p' /proc/cmdline)
part=${arg%%:*} new=${arg#*:}
case $new in '' | *[!0-9]*) say "bad lolssd.shrink=$arg"; exit 0 ;; esac
sys=/sys/class/block/$part
i=0
while [ ! -e "$sys" ] && [ $i -lt 30 ]; do sleep 1; i=$((i + 1)); done
[ -e "$sys/partition" ] || { say "no partition $part"; exit 0; }
disk=/dev/$(basename "$(readlink -f "$sys/..")")
num=$(cat "$sys/partition")
cur=$(cat "$sys/size")
[ "$cur" -ne "$new" ] || { say "/dev/$part is already $((new / 2097152)) GiB"; exit 0; }
[ "$new" -lt "$cur" ] || { say "refusing to grow /dev/$part"; exit 0; }

say "checking /dev/$part (1-3 min)..."
e2fsck -f -p "/dev/$part"
rc=$?
[ $rc -le 1 ] || { say "e2fsck exit $rc: nothing changed"; exit 0; }
say "shrinking the filesystem to $((new / 2097152)) GiB (5-20 min)..."
resize2fs -p "/dev/$part" "$((new / 2))K" || { say "resize2fs failed: partition untouched"; exit 0; }
say "shrinking partition $num on $disk..."
echo "size=$new" | sfdisk --no-reread --no-tell-kernel -N "$num" "$disk" ||
	{ say "sfdisk failed: the filesystem is smaller than its partition, harmless"; exit 0; }
# No re-read of the table here: it drops and re-adds the root's device node while systemd waits
# on it for /sysroot, the mount fails and the initramfs hangs in emergency mode. This boot
# keeps the old partition size in memory (the filesystem fits either way), the next one reads
# the new table.
say "done: /dev/$part is $((new / 2097152)) GiB, booting on"
