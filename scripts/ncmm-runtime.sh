#!/bin/sh
# CLI 和调度统一使用健康确认后的当前版本。
exec /usr/local/bin/ncmm-launcher exec -- "$@"
