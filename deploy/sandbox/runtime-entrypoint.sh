#!/bin/sh
set -eu
umask 077

term_requested=0
handle_term() {
  term_requested=1
}
trap handle_term TERM INT

while [ "$term_requested" -eq 0 ]; do
  sleep 3600 &
  wait "$!" || true
done
