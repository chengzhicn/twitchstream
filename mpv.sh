#!/bin/bash
url=${1:4}
mpv --quiet --force-window=immediate --idle=yes --demuxer-max-back-bytes=400MiB --demuxer-max-bytes=1950MiB "$url" >> /dev/null 2>&1
