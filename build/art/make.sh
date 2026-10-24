#!/usr/bin/env bash
# lolssd: значки и обои из SVG этой папки (результат хранится в репозитории, пересобирать — только
# после правки SVG). Нужны rsvg-convert и ImageMagick.
#   linux.ico, terminal.ico — значки ярлыков «Linux» и «Linux terminal» (16–256 px);
#   img0.jpg и img0_ШxВ.jpg, img100.jpg — запасные обои и экран блокировки: основные — обложка,
#   которую build/iso.sh скачивает при сборке (WALLPAPER).
set -euo pipefail
cd "$(dirname "$0")"
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
for i in linux terminal; do
	for s in 16 24 32 48 64 128 256; do
		rsvg-convert -w $s -h $s "$i.svg" -o "$t/$i-$s.png"
	done
	magick "$t/$i"-{16,24,32,48,64,128,256}.png "$i.ico"
done
rsvg-convert -w 3840 -h 2160 wallpaper.svg -o "$t/wall.png"
magick "$t/wall.png" -quality 92 img0.jpg
cp img0.jpg img100.jpg
for size in 1920x1080 1920x1200 2560x1440 2560x1600 3840x2160; do
	magick "$t/wall.png" -resize "${size}^" -gravity center -extent "$size" -quality 92 "img0_$size.jpg"
done
ls -la *.ico *.jpg
